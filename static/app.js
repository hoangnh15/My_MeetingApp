import * as tf from '@tensorflow/tfjs';
import '@tensorflow/tfjs-backend-webgl';
import '@mediapipe/selfie_segmentation';
import * as bodySegmentation from '@tensorflow-models/body-segmentation';

// Thiết lập backend cho TensorFlow.js
async function initTF() {
  await tf.setBackend('webgl');
  await tf.ready();
  console.log('TF.js backend:', tf.getBackend());
}
initTF();

// Ví dụ sử dụng bodySegmentation (blurVideoTrack như ở mẫu)
async function blurVideoTrack(originalVideoStreamTrack) {
  const segmenter = await bodySegmentation.createSegmenter(
    bodySegmentation.SupportedModels.MediaPipeSelfieSegmentation,
    {
      runtime: 'mediapipe',
      modelType: 'general',
      solutionPath: 'https://cdn.jsdelivr.net/npm/@mediapipe/selfie_segmentation', // Bạn vẫn có thể dùng URL này để load model của Mediapipe nếu cần
    }
  );

  const settings = originalVideoStreamTrack.getSettings();
  const h = settings.height || 480;
  const w = settings.width || 640;

  const video = document.createElement('video');
  video.height = h;
  video.width = w;
  video.muted = true;
  video.setAttribute('playsinline', '');
  const loaded = new Promise((res) =>
    video.addEventListener('loadedmetadata', res, { once: true })
  );
  const mediaStream = new MediaStream();
  mediaStream.addTrack(originalVideoStreamTrack);
  video.srcObject = mediaStream;
  video.play();
  await loaded;

  const canvas = document.createElement('canvas');
  const ctx = canvas.getContext('2d');
  canvas.height = h;
  canvas.width = w;

  async function drawBlur() {
    const segmentation = await segmenter.segmentPeople(video);
    const foregroundThreshold = 0.6;
    const backgroundBlurAmount = 12;
    const edgeBlurAmount = 3;
    const flipHorizontal = false;

    await bodySegmentation.drawBokehEffect(
      canvas,
      video,
      segmentation,
      foregroundThreshold,
      backgroundBlurAmount,
      edgeBlurAmount,
      flipHorizontal
    );
  }

  const blurredTrack = canvas.captureStream().getVideoTracks()[0];

  let t = -1;
  async function tick() {
    await drawBlur();
    t = window.setTimeout(tick, 1000 / 30);
  }
  await drawBlur();
  tick();

  blurredTrack.stop = () => {
    clearTimeout(t);
    MediaStreamTrack.prototype.stop.call(originalVideoStreamTrack);
  };

  originalVideoStreamTrack.addEventListener('ended', (e) => {
    blurredTrack.stop();
    blurredTrack.dispatchEvent(e);
  });

  blurredTrack.getSettings = () => originalVideoStreamTrack.getSettings();

  return blurredTrack;
}

// Các logic khác của app...
