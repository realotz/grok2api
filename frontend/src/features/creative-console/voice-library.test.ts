import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  hasVoiceLibraryPreview,
  previewLanguage,
  voiceLibraryGender,
  voiceLibraryTags,
  voiceLibraryTagsPhrase,
  voiceLibraryVoices,
  voiceSampleURL,
} from "./voice-library.ts";

describe("voice library", () => {
  it("covers the live Console roster with unique ids and gender", () => {
    const ids = voiceLibraryVoices.map((voice) => voice.id);
    assert.equal(ids.length, 28);
    assert.equal(new Set(ids).size, 28);
    assert.equal(voiceLibraryGender("eve"), "female");
    assert.equal(voiceLibraryGender("rex"), "male");
    assert.equal(voiceLibraryVoices.filter((voice) => voice.gender === "female").length, 10);
    assert.equal(voiceLibraryVoices.filter((voice) => voice.gender === "male").length, 18);
    assert.deepEqual([...voiceLibraryTags], ["pause", "whisper", "laugh", "slow", "soft", "cry", "angry"]);
    assert.equal(voiceLibraryTagsPhrase.zh.includes("你好，这里是声音世界。"), true);
    assert.equal(voiceLibraryTagsPhrase.zh.split("声音世界").length, 2);
  });

  it("maps preview language and sample URLs", () => {
    assert.equal(previewLanguage("zh"), "zh");
    assert.equal(previewLanguage("zh-CN"), "zh");
    assert.equal(previewLanguage("auto"), "zh");
    assert.equal(previewLanguage("en"), "en");
    assert.equal(previewLanguage("ja"), "en");
    assert.equal(voiceSampleURL("eve", "zh"), "/voice-library/eve/zh.mp3");
    assert.equal(voiceSampleURL("eve", "en"), "/voice-library/eve/en.mp3");
    assert.equal(hasVoiceLibraryPreview("eve"), true);
    assert.equal(hasVoiceLibraryPreview("missing"), false);
  });
});
