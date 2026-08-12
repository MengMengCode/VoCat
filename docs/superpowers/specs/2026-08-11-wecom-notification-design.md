# 企业微信消息推送设计

## 目标

新增独立的 `wecom` 通知通道，通过企业微信“消息推送（原群机器人）”Webhook 推送新收到的短信和自动任务执行结果。外部 API 契约与既有通知通道保持一致。

## 配置模型

`wecom` 配置包含：

- `enabled`：是否启用通道。
- `urls`：一个或多个企业微信消息推送 Webhook URL。Web 设置页将每个 URL
  显示为独立输入行，通过“添加 URL”按钮新增输入行、通过删除按钮移除输入行；
  不使用逗号、空格或换行分隔多个 URL。
- `payload_template`：完整 JSON 请求体模板。

Webhook URL 含有企业微信访问密钥，必须作为敏感配置存储、在读取接口中脱敏，并在日志和错误信息中避免泄露。URL 沿用现有出站 URL 校验与 SSRF 防护。

## 模板语义

用户在 Web 设置页编辑完整 JSON 请求体，以选择企业微信支持的任意消息格式，例如 `text`、`markdown`、`news` 或 `template_card`。

模板变量仅能作为 JSON 值出现，服务端使用 JSON 编码后的字符串替换，调用方不得在变量外添加引号。示例：

```json
{
  "msgtype": "text",
  "text": {
    "content": {{message}}
  }
}
```

可用变量：

- 通用：`{{event}}`、`{{title}}`、`{{message}}`、`{{timestamp}}`。
- 短信事件：`{{content}}`、`{{number}}`、`{{device_id}}`、`{{device_name}}`、`{{device_label}}`、`{{time}}`。

自动任务使用通用变量；短信专属变量在自动任务中替换为空字符串。模板渲染后必须为非空 JSON 对象，不得保留模板变量；无效模板在保存和测试时拒绝。

## 发送流程

短信分发器为 `wecom` 维护独立游标，发送失败不会阻塞其他通知渠道。自动任务完成后，和 Telegram、Bark、邮件、PushPlus、通用 Webhook 一样，向已启用的 `wecom` 通道发送结果。

发送器逐一 POST 渲染后的 JSON 到所有配置 URL，使用现有受限 HTTP 客户端。除 HTTP 2xx 外，企业微信返回 JSON 的 `errcode` 非零也视为服务商拒绝。

## Web 与 API

设置 API 将 `wecom` 加入已知通道和配置字段白名单，并提供 `POST /api/settings/notifications/wecom/test`。Web 设置页新增“企业微信”页签、启用开关、逐行编辑的 Webhook URL 列表、JSON 模板编辑器和测试按钮。

默认模板使用 `text` 消息，发送一条可辨识的测试内容。

## 验证

后端测试覆盖：配置字段验证、模板的 JSON 转义和拒绝无效模板、企业微信请求载荷、非零 `errcode` 失败处理、通知设置 API 读写与敏感 Webhook URL 保留。前端构建用于验证新增表单与类型契约。
