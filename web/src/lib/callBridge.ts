// Call media bridge: WebSocket (little-endian int16 PCM, 8 kHz mono) <-> WebAudio.
// Downlink: call audio -> browser speakers.
// Uplink: browser microphone -> call.
export interface CallBridgeOptions {
  onState?: (state: "idle" | "connecting" | "open" | "closed" | "error") => void;
  onError?: (message: string) => void;
}

const PCM_RATE = 8000;

export class CallMediaBridge {
  private ws: WebSocket | null = null;
  private ctx: AudioContext | null = null;
  private script: ScriptProcessorNode | null = null;
  private micStream: MediaStream | null = null;
  private micSource: MediaStreamAudioSourceNode | null = null;

  private queue: Int16Array[] = [];
  private queueBytes = 0;
  private micActive = false;
  private options: CallBridgeOptions;
  private gestureCleanup: (() => void) | null = null;

  constructor(options: CallBridgeOptions = {}) {
    this.options = options;
  }

  get connected(): boolean {
    return this.ws !== null && this.ws.readyState === WebSocket.OPEN;
  }

  private setState(state: "idle" | "connecting" | "open" | "closed" | "error") {
    this.options.onState?.(state);
  }

  async connect(url: string): Promise<void> {
    this.disconnect();
    this.setState("connecting");
    if (this.ctx === null) {
      try {
        this.ctx = new AudioContext({ sampleRate: PCM_RATE });
      } catch {
        this.ctx = new AudioContext();
      }
      // Browsers block audio until a user gesture; resume on the first
      // click/keystroke anywhere on the page so call audio can start.
      const resume = () => {
        if (this.ctx && this.ctx.state === "suspended") void this.ctx.resume();
      };
      window.addEventListener("pointerdown", resume, { passive: true });
      window.addEventListener("keydown", resume, { passive: true });
      this.gestureCleanup = () => {
        window.removeEventListener("pointerdown", resume);
        window.removeEventListener("keydown", resume);
      };
      this.script = this.ctx.createScriptProcessor(512, 1, 1);
      this.script.onaudioprocess = (event) => this.onAudioProcess(event);
      this.script.connect(this.ctx.destination);
    }
    await this.ctx.resume().catch(() => { /* gesture may be required; handled above */ });
    this.ws = new WebSocket(url);
    this.ws.binaryType = "arraybuffer";
    this.ws.onopen = () => this.setState("open");
    this.ws.onclose = () => this.setState("closed");
    this.ws.onerror = () => this.setState("error");
    this.ws.onmessage = (event) => this.onMessage(event.data);
  }

  /** Explicitly resume audio output (call from a user gesture). */
  resumeAudio() {
    if (this.ctx && this.ctx.state === "suspended") void this.ctx.resume();
  }

  disconnect() {
    if (this.ws) {
      this.ws.onopen = this.ws.onclose = this.ws.onerror = this.ws.onmessage = null;
      try { this.ws.close(); } catch { /* ignore */ }
      this.ws = null;
    }
    this.setMicActive(false);
    this.queue = [];
    this.queueBytes = 0;
  }

  async setMicActive(active: boolean): Promise<void> {
    this.micActive = active;
    if (!active) {
      if (this.micStream) {
        this.micStream.getTracks().forEach((track) => track.stop());
        this.micStream = null;
      }
      if (this.micSource && this.script) {
        try { this.micSource.disconnect(this.script); } catch { /* ignore */ }
      }
      this.micSource = null;
      return;
    }
    if (this.ctx === null || this.script === null || this.ws === null) return;
    try {
      this.micStream = await navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: true, noiseSuppression: true } });
      this.micSource = this.ctx.createMediaStreamSource(this.micStream);
      this.micSource.connect(this.script);
    } catch (error) {
      this.micActive = false;
      this.options.onError?.(`无法访问麦克风: ${error instanceof Error ? error.message : String(error)}`);
    }
  }

  private onMessage(data: unknown) {
    if (!(data instanceof ArrayBuffer) || data.byteLength === 0 || data.byteLength % 2 !== 0) return;
    const samples = new Int16Array(data);
    this.queue.push(samples);
    this.queueBytes += samples.length;
    // Cap the jitter buffer at ~200 ms (1600 samples) to keep latency bounded.
    while (this.queueBytes > 1600 && this.queue.length > 1) {
      const dropped = this.queue.shift();
      if (dropped) this.queueBytes -= dropped.length;
    }
  }

  private onAudioProcess(event: AudioProcessingEvent) {
    const output = event.outputBuffer.getChannelData(0);
    if (this.queue.length > 0) {
      let written = 0;
      while (written < output.length && this.queue.length > 0) {
        const chunk = this.queue[0];
        const take = Math.min(chunk.length, output.length - written);
        for (let i = 0; i < take; i++) {
          output[written + i] = chunk[i] / 32768;
        }
        written += take;
        this.queueBytes -= take;
        if (take === chunk.length) {
          this.queue.shift();
        } else {
          this.queue[0] = chunk.subarray(take);
        }
      }
    } else {
      output.fill(0);
    }

    if (this.micActive && this.ws && this.ws.readyState === WebSocket.OPEN) {
      const input = event.inputBuffer.getChannelData(0);
      const pcm = new Int16Array(input.length);
      for (let i = 0; i < input.length; i++) {
        const v = input[i] * 32768;
        pcm[i] = v < -32768 ? -32768 : v > 32767 ? 32767 : v | 0;
      }
      this.ws.send(pcm.buffer);
    }
  }

  destroy() {
    this.disconnect();
    this.gestureCleanup?.();
    this.gestureCleanup = null;
    if (this.script && this.ctx) {
      try { this.script.disconnect(this.ctx.destination); } catch { /* ignore */ }
    }
    this.script = null;
    if (this.ctx) {
      try { void this.ctx.close(); } catch { /* ignore */ }
    }
    this.ctx = null;
  }
}
