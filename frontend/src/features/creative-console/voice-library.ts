export const voiceLibraryPhrase = {
  zh: "你好，这里是声音世界。我可以小声说话，也可以开心的笑，慢慢地讲，也可以大声哭，生气发火。",
  en: "Hello, this is the world of sound. I can speak softly, laugh with joy, talk slowly, cry out loud, and get angry.",
} as const;

export const voiceLibraryTagsPhrase = {
  zh: "你好，这里是声音世界。[pause] 我可以<whisper>小声说话</whisper>，也可以开心的[laugh]笑，<slow><soft>慢慢地讲</soft></slow>，也可以[cry]大声哭，<angry>生气发火</angry>。",
  en: "Hello, this is the world of sound. [pause] I can <whisper>speak softly</whisper>, laugh [laugh] with joy, <slow><soft>talk slowly</soft></slow>, [cry] cry out loud, and <angry>get angry</angry>.",
} as const;

export const voiceLibraryTags = ["pause", "whisper", "laugh", "slow", "soft", "cry", "angry"] as const;

export const voiceLibraryVoices = [
  { id: "altair", name: "Altair", gender: "male" },
  { id: "ara", name: "Ara", gender: "female" },
  { id: "atlas", name: "Atlas", gender: "male" },
  { id: "aurora", name: "Aurora", gender: "female" },
  { id: "carina", name: "Carina", gender: "female" },
  { id: "castor", name: "Castor", gender: "male" },
  { id: "celeste", name: "Celeste", gender: "female" },
  { id: "cosmo", name: "Cosmo", gender: "male" },
  { id: "eve", name: "Eve", gender: "female" },
  { id: "helios", name: "Helios", gender: "male" },
  { id: "helix", name: "Helix", gender: "female" },
  { id: "iris", name: "Iris", gender: "female" },
  { id: "kepler", name: "Kepler", gender: "male" },
  { id: "leo", name: "Leo", gender: "male" },
  { id: "liora", name: "Liora", gender: "female" },
  { id: "lumen", name: "Lumen", gender: "male" },
  { id: "luna", name: "Luna", gender: "female" },
  { id: "lux", name: "Lux", gender: "male" },
  { id: "naksh", name: "Naksh", gender: "male" },
  { id: "orion", name: "Orion", gender: "male" },
  { id: "perseus", name: "Perseus", gender: "male" },
  { id: "rex", name: "Rex", gender: "male" },
  { id: "rigel", name: "Rigel", gender: "male" },
  { id: "sal", name: "Sal", gender: "male" },
  { id: "sirius", name: "Sirius", gender: "male" },
  { id: "ursa", name: "Ursa", gender: "female" },
  { id: "zagan", name: "Zagan", gender: "male" },
  { id: "zenith", name: "Zenith", gender: "male" },
] as const;

export type VoiceLibraryLang = keyof typeof voiceLibraryPhrase;
export type VoiceLibraryGender = (typeof voiceLibraryVoices)[number]["gender"];
export type VoiceLibraryVoice = (typeof voiceLibraryVoices)[number];

export function previewLanguage(language: string): VoiceLibraryLang {
  const value = language.trim().toLowerCase();
  if (value === "" || value === "auto" || value.startsWith("zh")) return "zh";
  return "en";
}

export function voiceSampleURL(voiceId: string, lang: VoiceLibraryLang): string {
  return `/voice-library/${encodeURIComponent(voiceId)}/${lang}.mp3`;
}

export function hasVoiceLibraryPreview(voiceId: string): boolean {
  return voiceLibraryVoices.some((voice) => voice.id === voiceId);
}

export function voiceLibraryGender(voiceId: string): VoiceLibraryGender | undefined {
  return voiceLibraryVoices.find((voice) => voice.id === voiceId)?.gender;
}
