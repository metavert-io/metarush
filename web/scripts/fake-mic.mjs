// The browser runs' microphone: Chromium's fake device (--use-fake-device-for-media-stream,
// fed with --use-file-for-fake-audio-capture), asked for by its own ID.
//
// Asked for the default microphone, Chromium's audio service opens this computer's real default
// input once to find out what "default" is, even with the fake device. When that device doesn't
// answer, getUserMedia never returns and the recorder never opens: on a headless test machine
// whose default input is a remote-desktop app's virtual microphone, that device can stop answering
// for a long time (every audio service that touches it hangs there) until someone connects.
// Asking for "Fake Audio Input 1" (the same fake device, playing the same file) leaves the
// computer's real input devices alone, so a stuck microphone can't stop a run.

/** Makes a browser context's getUserMedia ask for the fake microphone by its ID. */
export async function useFakeMicByID(ctx) {
  await ctx.addInitScript(() => {
    const md = navigator.mediaDevices;
    if (!md?.getUserMedia) return;
    const real = md.getUserMedia.bind(md);
    md.getUserMedia = async (c) => {
      const audio = c?.audio;
      if (audio === true || (audio && typeof audio === 'object' && !audio.deviceId)) {
        const devices = await md.enumerateDevices();
        const fake = devices.find((d) => d.kind === 'audioinput' && d.deviceId !== 'default' && /^Fake Audio Input/.test(d.label));
        if (fake) c = { ...c, audio: { ...(audio === true ? {} : audio), deviceId: { exact: fake.deviceId } } };
      }
      return real(c);
    };
  });
}
