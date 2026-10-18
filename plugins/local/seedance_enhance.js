export function extendSeedanceEnhanceMetadata(meta) {
  // Enhancement metadata lives separately from the upstream generation driver.
  // The host wraps generation and owns persistence, OSS and MediaKit requests.
  const seedanceEnhanceBaseModels = [
    "doubao-seedance-2-0-260128",
    "doubao-seedance-2-0-fast-260128",
    "doubao-seedance-2-0-mini-260615",
    "doubao-seedance-2-5-260628",
  ];
  const seedanceEnhanceModels = seedanceEnhanceBaseModels.map((name) => name + "-enhance");
  const seedanceEnhanceSchema = {
    seconds: { type: "number", unit: "second", description: { en: "Video generation unit price", zh: "视频生成单价" } },
    output_resolution: { enum: ["1080p", "2k"], description: { en: "Output resolution", zh: "输出分辨率" } },
    fps_tier: {
      enum: ["le30", "le60", "le120"],
      description: { en: "Output frame rate", zh: "输出帧率" },
      enumLabels: {
        le30: { en: "Up to 30 fps", zh: "不超过 30 帧" },
        le60: { en: "Over 30 to 60 fps", zh: "超过 30 至 60 帧" },
        le120: { en: "Over 60 to 120 fps", zh: "超过 60 至 120 帧" },
      },
    },
    video_input: {
      enum: ["none", "video"],
      description: { en: "Reference video", zh: "参考视频" },
      enumLabels: { none: { en: "Without reference video", zh: "无参考视频" }, video: { en: "With reference video", zh: "有参考视频" } },
    },
  };
  meta.models.push(...seedanceEnhanceModels);
  meta.usageProfiles.push({
    models: seedanceEnhanceModels,
    schema: seedanceEnhanceSchema,
    examples: [
      { label: "1080p · 10s · ≤30fps", facts: { seconds: 10, output_resolution: "1080p", fps_tier: "le30", video_input: "none" } },
      { label: "2K · 10.2s · ≤30fps", facts: { seconds: 10.2, output_resolution: "2k", fps_tier: "le30", video_input: "none" } },
    ],
  });
  for (const claim of meta.protocols) {
    if (claim.name === "openai_video") claim.models.push(...seedanceEnhanceModels);
  }
}
