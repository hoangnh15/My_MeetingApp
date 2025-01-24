package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const cloudflareAPIBase = "https://rtc.live.cloudflare.com/v1"
const appId = "731e0a4787f65a16e604a685bda2d9f5"
const appSecret = "431588a52895c60c3b862cd49bd8ec37db2401b7e4fe1b005f81792c3d12ec21"

type SessionDescription struct {
	SDP  string `json:"sdp"`
	Type string `json:"type"`
}

type CreateSessionResponse struct {
	SessionDescription SessionDescription `json:"sessionDescription"`
	SessionId          string             `json:"sessionId"`
}

type User struct {
	ID       string `json:"id"` // sessionID
	UserName string `json:"user_name"`
	Track0   string `json:"track_0"`
	Track1   string `json:"track_1"`
}

// In-memory storage for simplicity (replace with a database in production)
var users = make(map[string]User)   // Maps user ID to user
var rooms = make(map[string][]User) // Maps room ID to users
var clients = make(map[string]*websocket.Conn)
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Cho phép mọi nguồn gốc (có thể thay đổi để tăng cường bảo mật)
	},
}

// count
func countUsersInRoom(roomID string) int {
	// Kiểm tra xem room có tồn tại trong rooms không
	usersInRoom, exists := rooms[roomID]
	if !exists {
		// Nếu room không tồn tại, trả về 0
		return 0
	}

	// Trả về số người trong room
	return len(usersInRoom)
}

// Hàm gửi yêu cầu đến Cloudflare API

func sendRequest(url string, method string, body interface{}) ([]byte, error) {
	// Chuyển body thành JSON
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	// Tạo request
	req, err := http.NewRequest(method, url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, err
	}

	// Thêm header cho yêu cầu
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+appSecret)

	// Gửi request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Đọc dữ liệu phản hồi
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return respBody, nil
}

// CORS middleware để thêm header CORS vào các phản hồi
func handleCORS(c *gin.Context) {
	// Thêm các header CORS cho phép truy cập từ bất kỳ nguồn nào
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")

	// Nếu là preflight request (OPTIONS), chỉ cần trả về thành công
	if c.Request.Method == http.MethodOptions {
		c.JSON(http.StatusOK, gin.H{})
		c.Abort()
		return
	}
	c.Next()
}

// API: Tạo session mới
func createSession(c *gin.Context) {
	// Xử lý CORS
	handleCORS(c)

	// Lấy offer SDP từ client
	var offerSDP struct {
		SDP string `json:"sdp"`
	}
	if err := c.ShouldBindJSON(&offerSDP); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	// Tạo body gửi lên Cloudflare API
	body := map[string]interface{}{
		"sessionDescription": map[string]interface{}{
			"type": "offer",
			"sdp":  offerSDP.SDP,
		},
	}

	// Gửi yêu cầu tạo session đến Cloudflare
	url := fmt.Sprintf("%s/apps/%s/sessions/new", cloudflareAPIBase, appId)
	respBody, err := sendRequest(url, "POST", body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Error creating session: %v", err)})
		return
	}
	var obj CreateSessionResponse
	if err := json.Unmarshal([]byte(string(respBody)), &obj); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Unable to parse JSON"})
		return
	}
	// Parse respBody thành đối tượng JSON
	fmt.Println(obj)
	c.JSON(http.StatusOK, obj)
}

// --------------------------
func addTrack(c *gin.Context) {
	handleCORS(c)

	// Lấy sessionId từ query
	sessionId := c.DefaultQuery("sessionId", "")
	if sessionId == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sessionId is required"})
		return
	}

	// Định nghĩa kiểu dữ liệu cho request body
	var trackInfo struct {
		SessionDescription struct {
			Type string `json:"type"`
			SDP  string `json:"sdp"`
		} `json:"sessionDescription,omitempty"`
		Tracks []struct {
			Location  string `json:"location"`
			SessionId string `json:"sessionId,omitempty"`
			Mid       string `json:"mid"`
			TrackName string `json:"trackName"`
		} `json:"tracks"`
	}

	// Xử lý ràng buộc JSON
	if err := c.ShouldBindJSON(&trackInfo); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	fmt.Println("Received TrackInfo:", trackInfo)

	// Xây dựng danh sách các track cho API Cloudflare
	var tracks []map[string]interface{}
	for _, track := range trackInfo.Tracks {
		trackData := map[string]interface{}{
			"location":  track.Location,
			"mid":       track.Mid,
			"trackName": track.TrackName,
		}
		tracks = append(tracks, trackData)
	}
	fmt.Println("Prepared Tracks for Cloudflare:", tracks)

	// Tạo yêu cầu gửi lên API Cloudflare
	body := map[string]interface{}{
		"sessionDescription": map[string]interface{}{
			"type": trackInfo.SessionDescription.Type,
			"sdp":  trackInfo.SessionDescription.SDP,
		},
		"tracks": tracks,
	}

	url := fmt.Sprintf("%s/apps/%s/sessions/%s/tracks/new", cloudflareAPIBase, appId, sessionId)
	respBody, err := sendRequest(url, "POST", body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Error adding track: %v", err)})
		return
	}
	fmt.Println("Cloudflare API Response:", string(respBody))

	// Gửi kết quả trả về client
	c.Data(http.StatusOK, "application/json", respBody)
}

func getTrack(c *gin.Context) {
	handleCORS(c)

	// Lấy sessionId từ query
	sessionId := c.DefaultQuery("sessionId", "")
	if sessionId == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sessionId is required"})
		return
	}
	var requestData struct {
		SessionID string `json:"session_id"`
	}
	if err := c.ShouldBindJSON(&requestData); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body", "details": err.Error()})
		return
	}
	peerSessionID := requestData.SessionID

	// Truy xuất user từ peerSessionID
	user, exists := users[peerSessionID]
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	trackObjects := []map[string]string{
		{
			"location":  "remote",
			"sessionId": user.ID,
			"trackName": user.Track0,
		},
		{
			"location":  "remote",
			"sessionId": user.ID,
			"trackName": user.Track1,
		},
	}
	body := map[string]interface{}{
		"tracks": trackObjects,
	}
	url := fmt.Sprintf("%s/apps/%s/sessions/%s/tracks/new", cloudflareAPIBase, appId, sessionId)
	respBody, err := sendRequest(url, "POST", body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Error adding track: %v", err)})
		return
	}
	fmt.Println("Cloudflare API Response:", string(respBody))

	// Gửi kết quả trả về client
	c.Data(http.StatusOK, "application/json", respBody)

}

// -----------------------------
func renegotiateSession(c *gin.Context) {
	// Xử lý CORS
	handleCORS(c)

	sessionId := c.DefaultQuery("sessionId", "")

	var answerSDP struct {
		SDP string `json:"sdp"`
	}
	if err := c.ShouldBindJSON(&answerSDP); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	// Tạo body gửi lên Cloudflare API
	body := map[string]interface{}{
		"sessionDescription": map[string]interface{}{
			"type": "answer",
			"sdp":  answerSDP.SDP,
		},
	}

	// Gửi yêu cầu đàm phán lại session
	url := fmt.Sprintf("%s/apps/%s/sessions/%s/renegotiate", cloudflareAPIBase, appId, sessionId)
	respBody, err := sendRequest(url, "PUT", body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Error renegotiating session: %v", err)})
		return
	}

	// Gửi kết quả trả về từ Cloudflare về client
	c.Data(http.StatusOK, "application/json", respBody)
}

// ----------------------------
func getSessionId(c *gin.Context) {
	handleCORS(c)
	sessionId := c.DefaultQuery("sessionId", "")
	url := fmt.Sprintf("%s/apps/%s/sessions/%s", cloudflareAPIBase, appId, sessionId)
	const body = ""
	respBody, err := sendRequest(url, "GET", body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Error renegotiating session: %v", err)})
		return
	}

	// Gửi kết quả trả về từ Cloudflare về client
	c.Data(http.StatusOK, "application/json", respBody)
}

func leaveRoom(c *gin.Context) {
	handleCORS(c)

	// Retrieve user ID and room ID
	userID := c.Query("user_id")
	roomID := c.Query("room_id")

	if userID == "" || roomID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user_id and room_id are required"})
		return
	}

	// Remove user from room
	usersInRoom, exists := rooms[roomID]
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "Room not found"})
		return
	}

	for i, user := range usersInRoom {
		if user.ID == userID {
			rooms[roomID] = append(usersInRoom[:i], usersInRoom[i+1:]...)
			delete(users, userID) // Remove user from global map
			break
		}
	}

	// Notify remaining users in the room (if WebSocket is implemented)
	for _, u := range rooms[roomID] {
		if conn, ok := clients[u.ID]; ok {
			notification := gin.H{
				"type":    "a-peer-left",
				"message": "A peer has been left",
				"userId":  userID,
			}
			if err := conn.WriteJSON(notification); err != nil {
				fmt.Println("Failed to notify user:", u.ID, err)
			}

		}
		for _, u := range rooms[roomID] {
			if conn, ok := clients[u.ID]; ok {
				notification := gin.H{
					"type":    "count_partis",
					"message": "Number of member of the room",
					"count":   countUsersInRoom(roomID),
				}
				if err := conn.WriteJSON(notification); err != nil {
					fmt.Println("Failed to notify user:", u.ID, err)
				}
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "User left the room successfully"})
}

// -----------------------------
func main() {
	r := gin.Default()

	// Định nghĩa các endpoint
	r.POST("/sessions/new", createSession)
	r.POST("/sessions/tracks/new", addTrack)
	r.POST("/sessions/tracks/getNew", getTrack)
	r.PUT("/sessions/renegotiate", renegotiateSession)
	r.GET("/sessions/sessionId", getSessionId)
	r.GET("/sessions/TestCloseTrack", leaveRoom)
	//
	r.POST("/register", func(c *gin.Context) {
		var user User
		if err := c.ShouldBindJSON(&user); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		// Generate a unique user ID (for simplicity, use username as ID here)
		fmt.Println(user.ID, user.UserName, user.Track0, user.Track1)
		users[user.ID] = user
		c.JSON(http.StatusOK, gin.H{"message": "User registered successfully", "user": user})
	})
	r.GET("/api/ws", func(c *gin.Context) {
		// Upgrade HTTP connection to WebSocket
		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			fmt.Println("Failed to upgrade to WebSocket:", err)
			return
		}

		// Lấy userID từ query hoặc header (tuỳ cách bạn truyền userID)
		userID := c.Query("user_id")
		if userID == "" {
			conn.WriteMessage(websocket.TextMessage, []byte("user_id is required"))
			conn.Close()
			return
		}

		// Lưu kết nối WebSocket
		clients[userID] = conn
		defer func() {
			delete(clients, userID)
			conn.Close()
		}()

		// Lắng nghe tin nhắn từ client (nếu cần)
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				fmt.Println("WebSocket read error:", err)
				break
			}
			fmt.Printf("Received message from %s: %s\n", userID, string(message))
		}
	})
	r.POST("/create-room", func(c *gin.Context) {
		type RoomRequest struct {
			UserID string `json:"user_id"`
		}
		var roomReq RoomRequest
		if err := c.ShouldBindJSON(&roomReq); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if _, exists := users[roomReq.UserID]; !exists {
			c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
			return
		}
		roomID := uuid.New().String()
		rooms[roomID] = []User{users[roomReq.UserID]}
		fmt.Println("New RoomID:%s", roomID)
		c.JSON(http.StatusOK, gin.H{"message": "Room created successfully", "room_id": roomID})
	})

	r.POST("/join-room", func(c *gin.Context) {
		type JoinRoomRequest struct {
			RoomID string `json:"room_id"`
			UserID string `json:"user_id"`
		}

		var joinReq JoinRoomRequest
		if err := c.ShouldBindJSON(&joinReq); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		user, exists := users[joinReq.UserID]
		if !exists {
			c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
			return
		}

		// Thêm user vào room
		rooms[joinReq.RoomID] = append(rooms[joinReq.RoomID], user)

		// Gửi thông báo tới các client trong room (trừ người mới)
		for _, u := range rooms[joinReq.RoomID] {
			if u.ID != joinReq.UserID {
				if conn, ok := clients[u.ID]; ok {
					notification := gin.H{
						"type":    "new_peer_joined",
						"message": "New user joined the room",
						"user":    user,
					}
					if err := conn.WriteJSON(notification); err != nil {
						fmt.Println("Failed to notify user:", u.ID, err)
					}
				}
			}
		}
		//chuyển số người trong room cho các client:
		for _, u := range rooms[joinReq.RoomID] {
			if conn, ok := clients[u.ID]; ok {
				notification := gin.H{
					"type":    "count_partis",
					"message": "Number of member of the room",
					"count":   countUsersInRoom(joinReq.RoomID),
				}
				if err := conn.WriteJSON(notification); err != nil {
					fmt.Println("Failed to notify user:", u.ID, err)
				}
			}
		}
		// Gửi về cho client mới các peer video của pc đã ở trong phòng k phải bản thân:

		var userList []string
		for _, u := range rooms[joinReq.RoomID] {
			userList = append(userList, u.ID)
		}
		// Trả về phản hồi cho client mới
		c.JSON(http.StatusOK, gin.H{"message": "User joined room successfully", "room_id": joinReq, "userlist": userList})
	})

	r.POST("/leave-room", func(c *gin.Context) {
		type LeaveRoomRequest struct {
			RoomID string `json:"room_id"`
			UserID string `json:"user_id"`
		}
		var leaveReq LeaveRoomRequest
		if err := c.ShouldBindJSON(&leaveReq); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if _, exists := users[leaveReq.UserID]; !exists {
			c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
			return
		}
		// Remove user from the room
		for i, user := range rooms[leaveReq.RoomID] {
			if user.ID == leaveReq.UserID {
				rooms[leaveReq.RoomID] = append(rooms[leaveReq.RoomID][:i], rooms[leaveReq.RoomID][i+1:]...)
				break
			}
		}
		c.JSON(http.StatusOK, gin.H{"message": "User left room successfully", "room_id": leaveReq.RoomID})
	})
	r.Static("/static", "./static")

	r.Run(":8080") // Start server on port 8080
}
