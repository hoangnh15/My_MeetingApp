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

// UserShare lưu thông tin chia sẻ màn hình của 1 user
type UserShare struct {
	ID     string `json:"id"`      // session id, cũng coi là user id
	Track0 string `json:"track_0"` // tên track 0
	Track1 string `json:"track_1"` // tên track 1
}
type UserChat struct {
	ID               string `json:"id"`
	ChannelName      string `json:"dataChannelName"`
	AssociatedUserID string `json:"userid"`
}

// RoomShares quản lý thông tin chia sẻ màn hình theo room.
// Key: room id, Value: map từ user id đến thông tin UserShare.
var userChats = make(map[string]UserChat)
var roomShares = make(map[string]map[string]UserShare)
var roomChats = make(map[string][]UserChat)

// In-memory storage for simplicity (replace with a database in production)
var users = make(map[string]User) // Maps user ID to user
var userShares = make(map[string]UserShare)
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
	//fmt.Println(obj)
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
	//fmt.Println("Cloudflare API Response:", string(respBody))

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
		Type      string `json:"type"`
	}
	if err := c.ShouldBindJSON(&requestData); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body", "details": err.Error()})
		return
	}
	peerSessionID := requestData.SessionID
	reqType := requestData.Type
	var trackObjects []map[string]string

	if reqType == "normal" {
		// Xử lý kiểu normal: truy xuất từ map users
		user, exists := users[peerSessionID]
		if !exists {
			c.JSON(http.StatusNotFound, gin.H{
				"error": fmt.Sprintf("User not found: %s", peerSessionID),
			})
			return
		}

		trackObjects = []map[string]string{
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
	} else if reqType == "screen" {
		// Xử lý kiểu screen: truy xuất từ map userShares
		userShare, exists := userShares[peerSessionID]
		if !exists {
			c.JSON(http.StatusNotFound, gin.H{
				"error": fmt.Sprintf("UserShare not found: %s", peerSessionID),
			})
			return
		}

		trackObjects = []map[string]string{
			{
				"location":  "remote",
				"sessionId": userShare.ID,
				"trackName": userShare.Track0,
			},
			{
				"location":  "remote",
				"sessionId": userShare.ID,
				"trackName": userShare.Track1,
			},
		}
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid type provided; expected 'normal' or 'screen'"})
		return
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
	//fmt.Println("Cloudflare API Response:", string(respBody))

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
			break
		}
	}
	if rc, exists := roomChats[roomID]; exists {
		// Lọc bỏ các UserChat có AssociatedUserID trùng với userID
		newRC := []UserChat{}
		for _, chat := range rc {
			if chat.AssociatedUserID != userID {
				newRC = append(newRC, chat)
			}
		}
		roomChats[roomID] = newRC
	}

	// Notify remaining users in the room
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
			fmt.Println("Not found User join:", joinReq.UserID)
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
		var shareScreenUserList []string
		if shareMap, exists := roomShares[joinReq.RoomID]; exists {
			for id := range shareMap {
				shareScreenUserList = append(shareScreenUserList, id)
			}
		}
		// Trả về phản hồi cho client mới
		c.JSON(http.StatusOK, gin.H{"message": "User joined room successfully", "room_id": joinReq, "userlist": userList, "shareScreenUsers": shareScreenUserList})
	})

	r.POST("/ShareScreen", func(c *gin.Context) {
		var userData struct {
			ID     string `json:"id"`
			RoomID string `json:"roomid"`
			Track0 string `json:"track_0"`
			Track1 string `json:"track_1"`
		}
		if err := c.ShouldBindJSON(&userData); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Lưu thông tin chia sẻ màn hình vào map userShares
		userShares[userData.ID] = UserShare{
			ID:     userData.ID,
			Track0: userData.Track0,
			Track1: userData.Track1,
		}

		// Kiểm tra và khởi tạo map cho room nếu cần
		if _, exists := roomShares[userData.RoomID]; !exists {
			roomShares[userData.RoomID] = make(map[string]UserShare)
		}
		roomShares[userData.RoomID][userData.ID] = UserShare{
			ID:     userData.ID,
			Track0: userData.Track0,
			Track1: userData.Track1,
		}

		fmt.Printf("RoomShares updated for room %s: %+v\n", userData.RoomID, roomShares[userData.RoomID])

		// Gửi thông báo tới các client trong room (trừ người mới)
		for _, u := range rooms[userData.RoomID] {
			if u.ID != userData.ID {
				if conn, ok := clients[u.ID]; ok {
					notification := gin.H{
						"type":    "new_share_screen",
						"message": "New user share screen for the room",
						"user":    userData,
					}
					if err := conn.WriteJSON(notification); err != nil {
						fmt.Println("Failed to notify user:", u.ID, err)
					}
				}
			}
		}
		c.JSON(http.StatusOK, gin.H{"message": "User share screen successfully", "room_id": userData.RoomID})
	})
	//
	r.POST("/StopShareScreen", func(c *gin.Context) {
		// Định nghĩa kiểu nhận request JSON
		var requestData struct {
			UserID string `json:"user_id"`
			RoomID string `json:"room_id"`
		}

		// Bind JSON từ request body
		if err := c.ShouldBindJSON(&requestData); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Xóa thông tin share screen của user khỏi map userShares
		delete(userShares, requestData.UserID)

		// Nếu tồn tại thông tin share screen của room, xóa user đó ra khỏi map con
		if roomData, exists := roomShares[requestData.RoomID]; exists {
			delete(roomData, requestData.UserID)
			// Nếu sau khi xóa mà room không còn user nào chia sẻ màn hình, có thể xóa luôn room đó khỏi roomShares (tùy chọn)
			if len(roomData) == 0 {
				delete(roomShares, requestData.RoomID)
			}
		}

		// Gửi thông báo tới các client trong room (trừ người gửi yêu cầu) rằng user đã dừng share screen
		if usersInRoom, exists := rooms[requestData.RoomID]; exists {
			for _, u := range usersInRoom {
				if u.ID != requestData.UserID {
					if conn, ok := clients[u.ID]; ok {
						notification := gin.H{
							"type":    "share_screen_stopped",
							"message": "User stopped screen share",
							"userId":  requestData.UserID,
						}
						if err := conn.WriteJSON(notification); err != nil {
							fmt.Printf("Failed to notify user %s: %v\n", u.ID, err)
						}
					}
				}
			}
		}

		// Trả về phản hồi thành công cho client
		c.JSON(http.StatusOK, gin.H{
			"message": "Share screen stopped and info removed",
			"room_id": requestData.RoomID,
		})
	})
	r.POST("/NewChatSession", func(c *gin.Context) {
		// Xử lý CORS
		handleCORS(c)

		// Lấy offer SDP từ client
		var offerSDP struct {
			SDP string `json:"SDP"`
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
		c.JSON(http.StatusOK, obj)
	})
	r.POST("/newChannelSub", func(c *gin.Context) {
		sessionID := c.Query("sessionID")
		if sessionID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "sessionID is required"})
			return
		}
		// Bind JSON từ request body
		var requestData map[string]interface{}
		if err := c.ShouldBindJSON(&requestData); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body: " + err.Error()})
			return
		}
		url := fmt.Sprintf("%s/apps/%s/sessions/%s/datachannels/new", cloudflareAPIBase, appId, sessionID)

		// Gọi sendRequest để chuyển tiếp yêu cầu đến Cloudflare Calls
		respBody, err := sendRequest(url, "POST", requestData)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Error creating data channel: %v", err)})
			return
		}

		// Gửi phản hồi trả về client (đã được parse từ Cloudflare)
		c.Data(http.StatusOK, "application/json", respBody)

	})
	r.POST("/InitDcRoom", func(c *gin.Context) {
		// Xử lý CORS
		handleCORS(c)

		// Định nghĩa kiểu nhận request JSON từ FE
		var reqBody struct {
			ChatID      string `json:"chatid"`      // ID riêng cho UserChat
			ChannelName string `json:"channelname"` // Tên channel data
			SessionID   string `json:"sessionID"`   // ID của user (session ID)
			RoomID      string `json:"roomID"`      // ID của room
		}

		// Bind JSON từ request body
		if err := c.ShouldBindJSON(&reqBody); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body: " + err.Error()})
			return
		}

		// Tạo đối tượng UserChat từ dữ liệu nhận được
		userChat := UserChat{
			ID:               reqBody.ChatID,
			ChannelName:      reqBody.ChannelName,
			AssociatedUserID: reqBody.SessionID,
		}

		// Lưu thông tin vào map userChats (key là ChatID)
		userChats[reqBody.ChatID] = userChat

		// Kiểm tra và khởi tạo map con cho room nếu chưa có
		if _, exists := roomChats[reqBody.RoomID]; !exists {
			roomChats[reqBody.RoomID] = []UserChat{}
		}
		// Thêm thông tin UserChat vào danh sách trong room
		roomChats[reqBody.RoomID] = append(roomChats[reqBody.RoomID], userChat)

		// (Tùy chọn) In ra log để kiểm tra
		fmt.Printf("RoomChats updated for room %s: %+v\n", reqBody.RoomID, roomChats[reqBody.RoomID])
		// Gửi thông báo tới các client trong room (trừ người mới)
		for _, u := range rooms[reqBody.RoomID] {
			if u.ID != reqBody.SessionID {
				if conn, ok := clients[u.ID]; ok {
					notification := gin.H{
						"type":    "new_chat_join",
						"message": "New user chat for the room",
						"user":    reqBody,
					}
					if err := conn.WriteJSON(notification); err != nil {
						fmt.Println("Failed to notify user:", u.ID, err)
					}
				}
			}
		}

		// Gửi phản hồi thành công cho client khi init phòng
		c.JSON(http.StatusOK, gin.H{
			"message":   "Room initialized with chat info",
			"room_id":   reqBody.RoomID,
			"userChat":  userChat,
			"chat_list": roomChats[reqBody.RoomID],
		})
	})

	r.Static("/static", "./static")

	r.Run(":8080") // Start server on port 8080
}
