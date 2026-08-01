// Wails 运行时与后端绑定的类型化门面。
//
// Wails 在运行时向 window 注入两组全局：
//   - window.go.main.App.<Method>(...args)  → 调用 Go 侧绑定方法，返回 Promise
//   - window.runtime.EventsOn/EventsOff(...) → 订阅 Go 侧 EventsEmit 推来的事件
// 这里不依赖 wails generate 的产物，直接按稳定协议封装，前端可独立 tsc/vite build。

type GoApp = {
  OpenBook(dir: string): Promise<BookOpenResult>;
  NeedsSetup(): Promise<boolean>;
  ResumeBook(): Promise<void>;
  StartQuick(raw: string, reviewFirst: boolean, genre: StoryGenre): Promise<void>;
  Continue(text: string): Promise<void>;
  Steer(text: string): Promise<void>;
  Abort(): Promise<boolean>;
  Reopen(direction: string): Promise<void>;
  SetAdvanceMode(mode: string): Promise<void>;
  AdvanceOneChapter(): Promise<void>;
  GetSnapshot(): Promise<UISnapshot>;
  AnswerAskUser(
    id: string,
    answers: Record<string, string>,
    notes: Record<string, string>,
  ): Promise<void>;
  GetPendingAskUser(): Promise<AskRequest | null>;
  Version(): Promise<VersionInfo>;
  // 设置
  GetProviderPresets(): Promise<ProviderPreset[]>;
  SaveInitialConfig(input: InitialSetup): Promise<void>;
  GetConfig(): Promise<ConfigView>;
  SaveProvider(draft: ProviderDraft): Promise<void>;
  TestConnection(draft: ProviderDraft, modelName: string): Promise<void>;
  GetModels(): Promise<ModelsView>;
  SwitchModel(role: string, provider: string, model: string): Promise<void>;
  SetRoleThinking(role: string, level: string): Promise<void>;
  // 共创
  CoCreate(history: CoCreateMsg[]): Promise<CoCreateTurn>;
  StageCoCreate(history: CoCreateMsg[]): Promise<CoCreateTurn>;
  CancelCoCreateTurn(): Promise<void>;
  StartFromCoCreate(draft: string, reviewFirst: boolean, genre: StoryGenre): Promise<void>;
  PauseForCoCreate(): Promise<boolean>;
  ResumeFromCoCreate(draft: string): Promise<void>;
  CancelCoCreate(): Promise<void>;
  // 长任务与导出
  StartImport(opts: ImportOptions): Promise<void>;
  CancelImport(): Promise<void>;
  ImportResumeHint(): Promise<string>;
  StartSimulate(): Promise<void>;
  ImportSimulationProfile(path: string): Promise<void>;
  SimulateSourceDir(): Promise<string>;
  AddSimulationSources(): Promise<string[]>;
  OpenSimulationSourceDir(): Promise<string>;
  CancelSimulate(): Promise<void>;
  Export(opts: ExportOptions): Promise<ExportResult>;
  // 拆文（对标小说只读拆解）
  StartDeconstruct(opts: DeconstructOptions): Promise<void>;
  CancelDeconstruct(): Promise<void>;
  DeconstructResumeHint(libraryDir: string, bookName: string): Promise<string>;
  DeconstructLibraryPath(libraryDir: string, bookName: string): Promise<string>;
  // 扫榜（榜单趋势与选题决策）
  StartRankScan(opts: RankScanOptions): Promise<void>;
  CancelRankScan(): Promise<void>;
  RankScanResumeHint(libraryDir: string, platform: string, rankName: string, scanDate: string): Promise<string>;
  RankScanLibraryPath(libraryDir: string, platform: string, rankName: string, scanDate: string): Promise<string>;
  // 原生对话框
  PickImportFile(): Promise<string>;
  PickNovelFile(): Promise<string>;
  PickRankFile(): Promise<string>;
  PickProfileFile(): Promise<string>;
  PickExportPath(defaultName: string): Promise<string>;
  PickDirectory(title: string): Promise<string>;
  // 设定审阅
  GetFoundation(): Promise<FoundationView>;
  ReviseFoundation(instruction: string): Promise<void>;
  // 专项技能
  ListSkills(): Promise<SkillCatalog>;
  ReloadSkills(): Promise<SkillCatalog>;
  OpenSkillsDir(): Promise<string>;
  RunSkill(name: string, chapters: number[] | null): Promise<void>;
  // 章节阅读
  GetContents(): Promise<BookContents>;
  ReadChapter(chapter: number): Promise<ChapterText>;
  // 封面与生图
  GetImageGenSettings(): Promise<ImageGenSettings>;
  SaveImageGenSettings(draft: ImageGenDraft): Promise<void>;
  GetCover(): Promise<CoverInfo>;
  SuggestCoverPrompt(platform: string, genre: string, composition: string): Promise<string>;
  OptimizeCoverPrompt(current: string, platform: string, genre: string, composition: string): Promise<string>;
  GenerateCover(prompt: string, platform: string, genre: string, composition: string): Promise<CoverInfo>;
  CancelCover(): Promise<void>;
  CoverJobDir(): Promise<string>;
  ImportCoverFile(): Promise<CoverInfo>;
  RemoveCover(): Promise<void>;
  PreviewCoverTitle(layout: CoverTitleLayout): Promise<string>;
  ApplyCoverTitle(layout: CoverTitleLayout): Promise<CoverInfo>;
  // 书库
  ListBooks(): Promise<LibraryBook[] | null>;
  CreateBook(name: string, dir: string): Promise<string>;
  ForgetBook(path: string): Promise<void>;
  DefaultBooksDir(): Promise<string>;
  CurrentBookDir(): Promise<string>;
};

declare global {
  interface Window {
    go?: { main?: { App?: GoApp } };
    runtime?: {
      EventsOn(name: string, cb: (...data: any[]) => void): () => void;
      EventsOff(name: string): void;
    };
  }
}

function app(): GoApp {
  const a = window.go?.main?.App;
  if (!a) {
    throw new Error("Wails 后端未就绪（window.go.main.App 缺失）");
  }
  return a;
}

// ── 绑定方法 ──
export const OpenBook = (dir: string) => app().OpenBook(dir);
export const NeedsSetup = () => app().NeedsSetup();
export const ResumeBook = () => app().ResumeBook();
export const StartQuick = (raw: string, reviewFirst: boolean, genre: StoryGenre) =>
  app().StartQuick(raw, reviewFirst, genre);
export const Continue = (text: string) => app().Continue(text);
export const Steer = (text: string) => app().Steer(text);
export const Abort = () => app().Abort();
export const Reopen = (direction: string) => app().Reopen(direction);
export const SetAdvanceMode = (mode: string) => app().SetAdvanceMode(mode);
export const AdvanceOneChapter = () => app().AdvanceOneChapter();
export const GetSnapshot = () => app().GetSnapshot();
export const AnswerAskUser = (
  id: string,
  answers: Record<string, string>,
  notes: Record<string, string>,
) => app().AnswerAskUser(id, answers, notes);
export const GetPendingAskUser = () => app().GetPendingAskUser();
export const Version = () => app().Version();

// ── 设置 ──
export const GetProviderPresets = () => app().GetProviderPresets();
export const SaveInitialConfig = (input: InitialSetup) => app().SaveInitialConfig(input);
export const GetConfig = () => app().GetConfig();
export const SaveProvider = (draft: ProviderDraft) => app().SaveProvider(draft);
export const TestConnection = (draft: ProviderDraft, modelName: string) =>
  app().TestConnection(draft, modelName);
export const GetModels = () => app().GetModels();
export const SwitchModel = (role: string, provider: string, model: string) =>
  app().SwitchModel(role, provider, model);
export const SetRoleThinking = (role: string, level: string) =>
  app().SetRoleThinking(role, level);

// ── 共创 ──
export const CoCreate = (history: CoCreateMsg[]) => app().CoCreate(history);
export const StageCoCreate = (history: CoCreateMsg[]) => app().StageCoCreate(history);
export const CancelCoCreateTurn = () => app().CancelCoCreateTurn();
export const StartFromCoCreate = (draft: string, reviewFirst: boolean, genre: StoryGenre) =>
  app().StartFromCoCreate(draft, reviewFirst, genre);
export const PauseForCoCreate = () => app().PauseForCoCreate();
export const ResumeFromCoCreate = (draft: string) => app().ResumeFromCoCreate(draft);
export const CancelCoCreate = () => app().CancelCoCreate();

// ── 长任务与导出 ──
export const StartImport = (opts: ImportOptions) => app().StartImport(opts);
export const CancelImport = () => app().CancelImport();
export const ImportResumeHint = () => app().ImportResumeHint();
export const StartSimulate = () => app().StartSimulate();
export const ImportSimulationProfile = (path: string) => app().ImportSimulationProfile(path);
export const SimulateSourceDir = () => app().SimulateSourceDir();
export const AddSimulationSources = () => app().AddSimulationSources();
export const OpenSimulationSourceDir = () => app().OpenSimulationSourceDir();
export const CancelSimulate = () => app().CancelSimulate();
export const Export = (opts: ExportOptions) => app().Export(opts);

// ── 拆文 ──
export const StartDeconstruct = (opts: DeconstructOptions) => app().StartDeconstruct(opts);
export const CancelDeconstruct = () => app().CancelDeconstruct();
export const DeconstructResumeHint = (libraryDir: string, bookName: string) =>
  app().DeconstructResumeHint(libraryDir, bookName);
export const DeconstructLibraryPath = (libraryDir: string, bookName: string) =>
  app().DeconstructLibraryPath(libraryDir, bookName);

// ── 扫榜 ──
export const StartRankScan = (opts: RankScanOptions) => app().StartRankScan(opts);
export const CancelRankScan = () => app().CancelRankScan();
export const RankScanResumeHint = (libraryDir: string, platform: string, rankName: string, scanDate: string) =>
  app().RankScanResumeHint(libraryDir, platform, rankName, scanDate);
export const RankScanLibraryPath = (libraryDir: string, platform: string, rankName: string, scanDate: string) =>
  app().RankScanLibraryPath(libraryDir, platform, rankName, scanDate);

// ── 原生对话框 ──
export const PickImportFile = () => app().PickImportFile();
export const PickNovelFile = () => app().PickNovelFile();
export const PickRankFile = () => app().PickRankFile();
export const PickProfileFile = () => app().PickProfileFile();
export const PickExportPath = (defaultName: string) => app().PickExportPath(defaultName);
export const PickDirectory = (title: string) => app().PickDirectory(title);

// ── 设定审阅 ──
export const GetFoundation = () => app().GetFoundation();
export const ReviseFoundation = (instruction: string) => app().ReviseFoundation(instruction);
export const GetContents = () => app().GetContents();

// ── 专项技能 ──
export const ListSkills = () => app().ListSkills();
export const ReloadSkills = () => app().ReloadSkills();
export const OpenSkillsDir = () => app().OpenSkillsDir();
export const RunSkill = (name: string, chapters: number[] | null) =>
  app().RunSkill(name, chapters);
export const ReadChapter = (chapter: number) => app().ReadChapter(chapter);

// ── 封面与生图 ──
export const GetImageGenSettings = () => app().GetImageGenSettings();
export const SaveImageGenSettings = (draft: ImageGenDraft) => app().SaveImageGenSettings(draft);
export const GetCover = () => app().GetCover();
export const SuggestCoverPrompt = (platform: string, genre: string, composition: string) =>
  app().SuggestCoverPrompt(platform, genre, composition);
export const OptimizeCoverPrompt = (
  current: string,
  platform: string,
  genre: string,
  composition: string,
) => app().OptimizeCoverPrompt(current, platform, genre, composition);
export const GenerateCover = (
  prompt: string,
  platform: string,
  genre: string,
  composition: string,
) => app().GenerateCover(prompt, platform, genre, composition);
export const CancelCover = () => app().CancelCover();
export const CoverJobDir = () => app().CoverJobDir();
export const ImportCoverFile = () => app().ImportCoverFile();
export const RemoveCover = () => app().RemoveCover();
export const PreviewCoverTitle = (layout: CoverTitleLayout) => app().PreviewCoverTitle(layout);
export const ApplyCoverTitle = (layout: CoverTitleLayout) => app().ApplyCoverTitle(layout);

// ── 书库 ──
export const ListBooks = () => app().ListBooks();
export const CreateBook = (name: string, dir: string) => app().CreateBook(name, dir);
export const ForgetBook = (path: string) => app().ForgetBook(path);
export const DefaultBooksDir = () => app().DefaultBooksDir();
export const CurrentBookDir = () => app().CurrentBookDir();

// ── 事件订阅 ──
export function on(name: string, cb: (...data: any[]) => void): () => void {
  const rt = window.runtime;
  if (!rt) return () => {};
  return rt.EventsOn(name, cb);
}

// ── 类型（对应 Go 侧 DTO / host.UISnapshot 的子集，按需扩充） ──
export interface EngineEvent {
  id: string;
  time: string;
  finishedAt: string;
  failed: boolean;
  running: boolean;
  category: string;
  agent: string;
  summary: string;
  kind: string;
  level: string;
  depth: number;
  durationMs: number;
  retryAt: string;
}

export interface AskQuestion {
  question: string;
  header: string;
  options: { label: string; description: string }[];
  multiSelect: boolean;
}

export interface VersionInfo {
  Version: string;
  Commit: string;
  Date: string;
}

// ── 设置相关类型（Go 侧 settings.go 已带 json tag，故为小写驼峰） ──

export interface ProviderPreset {
  name: string;
  label: string;
  baseURL: string;
  needType: boolean;
  apiKeyOptional: boolean;
}

export interface InitialSetup {
  provider: string;
  customName: string;
  type: string;
  apiKey: string;
  baseURL: string;
  model: string;
}

export interface ModelView {
  name: string;
  contextWindow: number;
  references: string[] | null;
}

export interface ProviderView {
  name: string;
  type: string;
  api: string;
  baseURL: string;
  models: ModelView[] | null;
  hasAPIKey: boolean;
  apiKeyHint: string;
  requiresAPIKey: boolean;
}

export interface ConfigView {
  providers: ProviderView[] | null;
  defaultProvider: string;
  defaultModel: string;
  configPath: string;
}

export type APIKeyAction = "keep" | "replace" | "clear";

export interface ProviderDraft {
  provider: string;
  type: string;
  api: string;
  baseURL: string;
  models: { name: string; contextWindow: number }[];
  renames: { from: string; to: string }[];
  apiKeyAction: APIKeyAction;
  apiKey: string;
}

export interface ModelOption {
  name: string;
  contextWindow: number;
  contextSource: string;
}

export interface RoleSelection {
  role: string;
  provider: string;
  model: string;
  explicit: boolean;
  thinking: string;
  available: string[] | null;
}

export interface ModelsView {
  providers: string[] | null;
  models: Record<string, ModelOption[]> | null;
  roles: RoleSelection[] | null;
}

// ── 共创 ──

export interface CoCreateMsg {
  role: "user" | "assistant";
  content: string;
}

export type StoryGenre = "novel" | "short_story";

export interface CoCreateTurn {
  message: string;
  prompt: string;
  ready: boolean;
  suggestions: string[] | null;
  raw: string;
}

// ── 长任务与导出 ──

export interface ImportOptions {
  sourcePath: string;
  autoConfirm: boolean;
  storyResolution: "" | "open" | "closed";
  continueAfter: boolean;
  guidance: string;
  acceptSegmentation: boolean;
}

export interface JobEvent {
  stage: string;
  current: number;
  total: number;
  message: string;
  level: string;
  key: string;
  retryAt: string;
  error: string;
  continued: boolean;
  paused: boolean;
}

export interface JobDone {
  paused?: boolean;
  stage: string;
  continued?: boolean;
  error: string;
}

export interface DeconstructOptions {
  sourcePath: string;
  libraryDir: string;
  bookName: string;
  form: "" | "long" | "short";
  acceptPreview: boolean;
  autoConfirm: boolean;
  guidance: string;
  retryFailed: boolean;
}

// RipDone 的 degraded/failed 承载「有章节重试后仍失败，产物不完整但可用」。
export interface RipDone extends JobDone {
  degraded?: boolean;
  failed?: number[];
}

export interface RankScanOptions {
  pastedText: string;
  filePath: string;
  dirPath: string;
  platform: string;
  rankName: string;
  libraryDir: string;
  scanDate: string;
}

// ScanDone 的 sparse 承载「有效条目不足阈值，结论参考价值有限」。
export interface ScanDone extends JobDone {
  sparse?: boolean;
  entries?: number;
  dir?: string;
}

export interface ExportOptions {
  format: "" | "txt" | "epub";
  outPath: string;
  from: number;
  to: number;
  overwrite: boolean;
}

export interface ExportResult {
  path: string;
  chapters: number;
  bytes: number;
  skipped: number[] | null;
}

// ── 设定审阅 ──

export interface FoundationView {
  premise: string;
  outline:
    | { chapter: number; title: string; coreEvent: string; hook: string; scenes: string[] | null }[]
    | null;
  characters:
    | {
        name: string;
        aliases: string[] | null;
        role: string;
        description: string;
        arc: string;
        traits: string[] | null;
        tier: string;
      }[]
    | null;
  worldRules: { category: string; rule: string; boundary: string }[] | null;
  compass: { endingDirection: string; openThreads: string[] | null; estimatedScale: string } | null;
  volumes: { index: number; title: string; theme: string }[] | null;
  awaitingReview: boolean;
  nextChapter: number;
}

// ── 专项技能 ──

export interface SkillItem {
  name: string;
  description: string;
  agent: string;
  scope: string;
  source: string;
  body: string;
}

export interface SkillProblem {
  source: string;
  err: string;
}

export interface SkillCatalog {
  skills: SkillItem[] | null;
  dir: string;
  problems: SkillProblem[] | null;
}

// ── 章节阅读 ──

export interface ChapterMeta {
  chapter: number;
  title: string;
  words: number;
  volume: number;
  volumeTitle: string;
}

export interface BookContents {
  novelName: string;
  chapters: ChapterMeta[] | null;
  totalWords: number;
  layered: boolean;
}

export interface ChapterText {
  chapter: number;
  title: string;
  text: string;
  words: number;
  prevChapter: number;
  nextChapter: number;
}

// ── 封面与生图 ──

export interface ImageGenSettings {
  baseURL: string;
  model: string;
  size: string;
  hasAPIKey: boolean;
  apiKeyHint: string;
  path: string;
}

export interface ImageGenDraft {
  baseURL: string;
  model: string;
  size: string;
  apiKeyAction: APIKeyAction;
  apiKey: string;
}

export interface CoverInfo {
  exists: boolean;
  dataURL: string;
  path: string;
  prompt: string;
  updatedAt: string;
  // hasBase = 存在未叠字的原图，也即改排版不用重新生图。
  hasBase: boolean;
  layout: CoverTitleLayout;
  preset: string;
  platform: string;
  genre: string;
  resolvedGenre: string;
  composition: string;
  platformPath: string;
  hasPlatformArtifact: boolean;
}

// CoverTitleLayout 是封面叠字的排版参数。书名由本地字体排上去，不交给生图模型
// （中文字形在生图模型里几乎必糊），所以改这里不花钱、不需要重新出图。
export interface CoverTitleLayout {
  enabled: boolean;
  title: string;
  author: string;
  position: "top" | "center" | "bottom";
  // scale 是相对基准字号的倍率，后端收敛到 0.5–2。
  scale: number;
  theme: "light" | "dark";
  font: "hei" | "song" | "kai";
  style: "auto" | "gold" | "modern" | "romance" | "thriller" | "scifi" | "literary";
}

// cover:progress 生图心跳（每秒一次，发起时立即先发一次）。
// bookDir 用于识别是哪本书在生图——切到别的书会重建 Host 并取消在途生图。
export interface CoverProgress {
  elapsedSec: number;
  budgetSec: number;
  bookDir: string;
}

// cover:done 生图收尾（成功、失败、被取消都会发），用于清掉"生图中"状态。
export interface CoverDone {
  bookDir: string;
}

// ── 书库 ──

export interface LibraryBook {
  path: string;
  name: string;
  lastOpened: string;
  chapters: number;
  words: number;
  phase: string;
  costUSD: number;
  missing: boolean;
  coverURL: string;
}

export interface AskRequest {
  id: string;
  questions: AskQuestion[];
}

export interface BookOpenResult {
  phase: string;
  hasProgress: boolean;
  recoveryLabel: string;
  isRunning: boolean;
}

export interface OutlineEntry {
  Chapter: number;
  Title: string;
  CoreEvent: string;
}

export interface AgentContext {
  Tokens: number;
  ContextWindow: number;
  Percent: number;
  Scope: string;
  Strategy: string;
  ActiveMessages: number;
  SummaryMessages: number;
  CompactedCount: number;
  KeptCount: number;
}

export interface AgentSnapshot {
  Name: string;
  State: string;
  TaskID: string;
  TaskKind: string;
  Summary: string;
  Tool: string;
  Turn: number;
  Context: AgentContext;
  UpdatedAt: string;
}

export interface AgentCacheStat {
  Role: string;
  Model: string;
  Input: number;
  Output: number;
  CacheRead: number;
  CacheWrite: number;
  Cost: number;
  Saved: number;
  CacheCapable: boolean;
  RecentCacheRead: number;
  RecentInput: number;
  RecentSamples: number;
}

// UISnapshot 映射 host.UISnapshot（internal/host/events.go）。字段名沿用 Go 的大写导出名，
// 因为 Wails 直接 JSON 序列化结构体、这些字段没有 json tag。
export interface UISnapshot {
  Provider: string;
  NovelName: string;
  ModelName: string;
  ModelContextWindow: number;
  ThinkingLevel: string;
  Style: string;
  RuntimeState: string;
  StatusLabel: string;
  Phase: string;
  Flow: string;
  CurrentChapter: number;
  TotalChapters: number;
  CompletedCount: number;
  TotalWordCount: number;
  InProgressChapter: number;
  PendingRewrites: number[] | null;
  RewriteReason: string;
  PendingSteer: string;
  AdvanceMode: string;
  AdvancePermitChapter: number;
  HasAdvanceHold: boolean;
  AdvanceHoldReason: string;
  RecoveryLabel: string;
  IsRunning: boolean;
  Agents: AgentSnapshot[] | null;

  // 用量
  TotalInputTokens: number;
  TotalOutputTokens: number;
  TotalCacheReadTokens: number;
  TotalCacheWriteTokens: number;
  TotalCostUSD: number;
  TotalSavedUSD: number;
  BudgetLimitUSD: number;

  // 缓存诊断
  OverallCacheCapable: boolean;
  OverallRecentCacheRead: number;
  OverallRecentInput: number;
  OverallRecentSamples: number;
  TotalCacheBreaks: number;
  MissingAssistantUsage: number;
  CachePerAgent: AgentCacheStat[] | null;
  CachePerModel: AgentCacheStat[] | null;

  // 设定
  Premise: string;
  Outline: OutlineEntry[] | null;
  Characters: string[] | null;
  SupportingCount: number;
  RecentSupporting: string[] | null;
  Layered: boolean;
  CurrentVolumeArc: string;
  NextVolumeTitle: string;
  CompassDirection: string;
  CompassScale: string;

  // 详情
  LastCommitSummary: string;
  LastReviewSummary: string;
  LastCheckpointName: string;
  RecentSummaries: string[] | null;
}
