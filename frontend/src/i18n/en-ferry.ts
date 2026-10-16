/**
 * 英文词典 · ferry 页面分片。key = 中文原文。
 */
export const en: Record<string, string> = {
  // 侧栏与页面标题
  免费额度: "Free quota",
  "把 OpenCode 的免费模型摆渡进本网关 · 免费由价格判定，可用由探测判定 · 当前可提供 {n} 个模型":
    "Ferries OpenCode's free models into this gateway · free by price, usable by probe · {n} models currently offered",

  // 头部操作
  读取模型: "Read models",
  重新检测: "Re-probe",
  启用: "Enable",
  停用: "Disable",
  操作失败: "Operation failed",

  // 运行状态
  运行状态: "Status",
  "隔离运行时托管在数据目录的 ferry/ 下，与系统上其它 OpenCode 互不影响":
    "The isolated runtime lives in ferry/ under the data directory and does not touch any other OpenCode on this machine",
  说明: "Detail",
  模型总数: "Catalog",
  可用模型: "Available",
  "隔离 OpenCode": "Isolated OpenCode",
  系统代理: "System proxy",
  跟随: "Follow",
  直连: "Direct",
  "检测进行中：正在检测 {a} 个，剩余 {b} 个": "Probing: {a} in flight, {b} pending",
  "改为直连": "Use direct connection",
  "跟随系统代理": "Follow system proxy",
  "下载隔离运行时与访问免费模型时用；切换会重启隔离服务":
    "Used for downloading the isolated runtime and reaching the free models; switching restarts the isolated service",

  // 阶段
  未启用: "Not enabled",
  启动中: "Starting",
  准备运行时: "Preparing runtime",
  检测中: "Probing",
  运行中: "Running",
  出错: "Error",

  // 模型表
  免费模型: "Free models",
  "通过的模型会成为本网关的模型：既出现在 /v1/models，也出现在「智能体接入」与「密钥」的模型选择里；取消勾选即不再对外提供":
    "Probed models become models of this gateway: they show up in /v1/models and in the model pickers of Agent integration and Keys; unchecking stops offering them",
  "可用 {n} 个": "{n} available",
  模型: "Model",
  能力: "Capability",
  判定: "Verdict",
  耗时: "Duration",
  对外提供: "Offered",
  工具: "Tools",
  仅对话: "Chat only",
  待检测: "Not probed yet",
  推理: "Reasoning",
  图片: "Images",
  上下文: "context",
  缓存判定: "from cache",
  已勾选: "Offered",
  未勾选: "Withheld",
  "勾选/取消对外提供": "Toggle offering this model",
  "未通过检测的模型不对外提供": "A model that failed the probe is never offered",
  "还没有读取到免费模型：点「读取模型」重试，或查看上方状态里的错误":
    "No free model read yet: retry with “Read models”, or check the error above",
  "未启用：点右上角「启用」开始（首次会在数据目录里下载隔离 OpenCode 运行时）":
    "Not enabled: press “Enable” above (the first run downloads the isolated OpenCode runtime into the data directory)",

  // 判定类别
  可用: "Available",
  额度用尽: "Quota exhausted",
  限流: "Rate limited",
  无权限: "No access",
  超时: "Timeout",
  失败: "Failed",
};
