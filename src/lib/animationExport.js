function downloadBlob(blob, filename) {
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  link.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

function wait(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

function createCaptureCanvas(video, target, maxWidth, region) {
  const targetRect = target.getBoundingClientRect();
  const rect = region
    ? {
        left: targetRect.left + region.x,
        top: targetRect.top + region.y,
        width: region.width,
        height: region.height,
      }
    : targetRect;
  const scale = Math.min(1, maxWidth / rect.width);
  const canvas = document.createElement("canvas");
  canvas.width = Math.max(1, Math.round(rect.width * scale));
  canvas.height = Math.max(1, Math.round(rect.height * scale));
  const context = canvas.getContext("2d", {
    alpha: false,
    willReadFrequently: true,
  });
  const sourceScaleX = video.videoWidth / window.innerWidth;
  const sourceScaleY = video.videoHeight / window.innerHeight;

  const draw = () => {
    context.drawImage(
      video,
      rect.left * sourceScaleX,
      rect.top * sourceScaleY,
      rect.width * sourceScaleX,
      rect.height * sourceScaleY,
      0,
      0,
      canvas.width,
      canvas.height
    );
  };

  return { canvas, context, draw };
}

async function recordWebM({ canvas, draw, duration, fps }) {
  const mimeType = [
    "video/webm;codecs=vp9",
    "video/webm;codecs=vp8",
    "video/webm",
  ].find((type) => MediaRecorder.isTypeSupported(type));

  if (!mimeType) {
    throw new Error("This browser cannot encode WebM video.");
  }

  const stream = canvas.captureStream(fps);
  const recorder = new MediaRecorder(stream, {
    mimeType,
    videoBitsPerSecond: 5_000_000,
  });
  const chunks = [];
  recorder.ondataavailable = (event) => {
    if (event.data.size) chunks.push(event.data);
  };
  const stopped = new Promise((resolve) => {
    recorder.onstop = resolve;
  });

  recorder.start(250);
  const startedAt = performance.now();

  await new Promise((resolve) => {
    const render = (now) => {
      draw();
      if (now - startedAt >= duration * 1000) {
        resolve();
      } else {
        requestAnimationFrame(render);
      }
    };
    requestAnimationFrame(render);
  });

  recorder.stop();
  await stopped;
  stream.getTracks().forEach((track) => track.stop());
  return new Blob(chunks, { type: mimeType });
}

async function recordGif({ canvas, context, draw, duration, fps }) {
  const { GIFEncoder, quantize, applyPalette } = await import("gifenc");
  const gif = GIFEncoder();
  const frameDelay = Math.round(1000 / fps);
  const frameCount = Math.max(1, Math.round(duration * fps));

  for (let frame = 0; frame < frameCount; frame++) {
    const frameStartedAt = performance.now();
    draw();
    const rgba = context.getImageData(
      0,
      0,
      canvas.width,
      canvas.height
    ).data;
    const palette = quantize(rgba, 256);
    const indexed = applyPalette(rgba, palette);
    gif.writeFrame(indexed, canvas.width, canvas.height, {
      palette,
      delay: frameDelay,
      repeat: 0,
    });
    await wait(Math.max(0, frameDelay - (performance.now() - frameStartedAt)));
  }

  gif.finish();
  return new Blob([gif.bytes()], { type: "image/gif" });
}

export async function exportAnimation({
  target,
  format,
  duration,
  fps,
  region,
  onCaptureChange,
}) {
  if (!navigator.mediaDevices?.getDisplayMedia) {
    throw new Error("Screen capture is not supported in this browser.");
  }

  const displayStream = await navigator.mediaDevices.getDisplayMedia({
    video: {
      displaySurface: "browser",
      frameRate: fps,
    },
    audio: false,
    preferCurrentTab: true,
    selfBrowserSurface: "include",
  });
  const video = document.createElement("video");

  try {
    video.srcObject = displayStream;
    video.muted = true;
    await video.play();
    onCaptureChange(true);
    await wait(250);

    const capture = createCaptureCanvas(
      video,
      target,
      format === "gif" ? 800 : 1920,
      region
    );
    const blob = format === "gif"
      ? await recordGif({ ...capture, duration, fps })
      : await recordWebM({ ...capture, duration, fps });

    downloadBlob(
      blob,
      `flowdraw-${new Date().toISOString().slice(0, 19)
        .replaceAll(":", "-")}.${format}`
    );
  } finally {
    onCaptureChange(false);
    displayStream.getTracks().forEach((track) => track.stop());
    video.srcObject = null;
  }
}
