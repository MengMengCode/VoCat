# 企业微信消息推送实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 增加可配置 JSON 请求模板的企业微信 Webhook 通知通道，向新短信和自动任务结果发送消息。

**架构：** 新建专注的企业微信通知模块，统一构建事件变量、JSON 安全替换、Webhook POST 和 `errcode` 响应判定。设置 API 将 `wecom` 纳入白名单、保密 URL 与连通性测试；短信和自动任务分发器只增加该通道分支。前端在现有通知设置表单中新增企业微信页签和请求体编辑器。

**技术栈：** Go 1.25、标准库 `net/http` 与 `encoding/json`、SQLite 通知设置、React、TypeScript、Vite。

---

## 文件结构

- 创建：`internal/server/wecom_notification.go`，渲染企业微信 JSON 模板、创建安全 HTTP 请求并判定企业微信响应。
- 创建：`internal/server/wecom_notification_test.go`，覆盖 JSON 转义、模板拒绝和企业微信响应失败。
- 修改：`internal/server/settings_api.go`，登记 `wecom` 配置字段、启用连通性测试并调用企业微信发送器。
- 修改：`internal/server/settings_api_test.go`，验证企业微信配置 API、敏感 URL 与测试路径。
- 修改：`internal/store/settings.go`，将 `wecom.urls` 注册为敏感字段。
- 修改：`internal/server/sms_notifications.go`，将新短信事件接入企业微信通道。
- 修改：`internal/server/sms_notifications_test.go`，覆盖企业微信短信配置要求和变量数据。
- 修改：`internal/server/automatic_task_notifications.go`，将自动任务结果接入企业微信通道。
- 修改：`web/src/types.ts`，扩展通知设置类型。
- 修改：`web/src/components/settings/model.ts`，增加企业微信表单、默认模板、读取和提交映射。
- 修改：`web/src/components/settings/PushTabs.tsx`，新增企业微信配置界面。
- 修改：`web/src/pages/SettingsPage.tsx`，增加页签、测试状态与测试请求。

### 任务 1：企业微信模板与响应判定

**文件：**
- 创建：`internal/server/wecom_notification_test.go`
- 创建：`internal/server/wecom_notification.go`

- [ ] **步骤 1：编写失败的模板与响应测试**

```go
func TestRenderWecomPayloadEscapesTemplateValues(t *testing.T) {
    payload, err := renderWecomPayload(
        `{"msgtype":"text","text":{"content":{{message}},"number":{{number}}}}`,
        wecomTemplateValues{"message": "quote: \\"\\nline", "number": "+447386"},
    )
    if err != nil { t.Fatal(err) }
    if got := string(payload); got != `{"msgtype":"text","text":{"content":"quote: \\"\\nline","number":"+447386"}}` {
        t.Fatalf("payload = %s", got)
    }
}

func TestRenderWecomPayloadRejectsUnknownVariableAndNonObject(t *testing.T) {
    for _, template := range []string{`{"text":{{unknown}}}`, `[]`} {
        if _, err := renderWecomPayload(template, wecomTemplateValues{}); err == nil {
            t.Fatalf("template %q was accepted", template)
        }
    }
}

func TestValidateWecomResponseRejectsProviderError(t *testing.T) {
    if err := validateWecomResponse(http.StatusOK, []byte(`{"errcode":40058,"errmsg":"invalid"}`)); !errors.Is(err, errProviderRejected) {
        t.Fatalf("error = %v", err)
    }
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/server -run 'TestRenderWecomPayload|TestValidateWecomResponse' -count=1`

预期：FAIL，提示 `renderWecomPayload`、`wecomTemplateValues` 和 `validateWecomResponse` 未定义。

- [ ] **步骤 3：实现最少的模板与响应代码**

在 `internal/server/wecom_notification.go` 中定义受支持变量列表，先用 `json.Marshal` 编码每个字符串，再替换精确的 `{{name}}` 标记；若保留任何 `{{` 或 `}}`，或者 `json.Unmarshal` 后不是非空 `map[string]json.RawMessage`，返回错误。响应处理必须要求 HTTP 2xx、可解析 JSON，且 `errcode` 为零。

```go
type wecomTemplateValues map[string]string

func renderWecomPayload(template string, values wecomTemplateValues) ([]byte, error) {
    for _, name := range wecomTemplateVariableNames {
        encoded, _ := json.Marshal(values[name])
        template = strings.ReplaceAll(template, "{{"+name+"}}", string(encoded))
    }
    if strings.Contains(template, "{{") || strings.Contains(template, "}}") {
        return nil, errors.New("wecom.payload_template contains an unsupported variable")
    }
    var payload map[string]json.RawMessage
    if err := json.Unmarshal([]byte(template), &payload); err != nil || len(payload) == 0 {
        return nil, errors.New("wecom.payload_template must render to a non-empty JSON object")
    }
    return []byte(template), nil
}

func validateWecomResponse(status int, body []byte) error {
    var result struct { ErrCode int `json:"errcode"` }
    if status < http.StatusOK || status >= http.StatusMultipleChoices || json.Unmarshal(body, &result) != nil || result.ErrCode != 0 {
        return fmt.Errorf("%w: WeCom response was not successful", errProviderRejected)
    }
    return nil
}

func wecomTestValues(now time.Time) wecomTemplateValues {
    return wecomTemplateValues{
        "event": "test", "title": "vocat", "message": "vocat notification test",
        "timestamp": now.UTC().Format(time.RFC3339),
    }
}

func sendWecomNotification(ctx context.Context, config map[string]any, values wecomTemplateValues) error {
    payload, err := renderWecomPayload(configString(config, "payload_template"), values)
    if err != nil { return err }
    client, err := restrictedHTTPClient(ctx, 8*time.Second, "")
    if err != nil { return err }
    for _, destination := range configStrings(config, "urls") {
        parsed, err := validateOutboundURL(ctx, destination, false)
        if err != nil { return err }
        request, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), bytes.NewReader(payload))
        if err != nil { return fmt.Errorf("create WeCom notification request: %w", err) }
        request.Header.Set("Content-Type", "application/json; charset=utf-8")
        request.Header.Set("User-Agent", "vocat-wecom-notification/1")
        response, err := client.Do(request)
        if err != nil { return fmt.Errorf("send WeCom notification: %w", err) }
        body, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10)); response.Body.Close()
        if readErr != nil { return fmt.Errorf("read WeCom response: %w", readErr) }
        if err := validateWecomResponse(response.StatusCode, body); err != nil { return err }
    }
    return nil
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/server -run 'TestRenderWecomPayload|TestValidateWecomResponse' -count=1`

预期：PASS。

- [ ] **步骤 5：提交本任务**

运行：`git add internal/server/wecom_notification.go internal/server/wecom_notification_test.go && git commit -m "feat: add WeCom payload renderer"`

预期：创建包含模板渲染和响应判定的提交。若 Git 作者身份仍未配置，停止提交但保留已验证的工作区改动，不自行设置身份。

### 任务 2：设置 API 与敏感 Webhook URL

**文件：**
- 修改：`internal/server/settings_api_test.go`
- 修改：`internal/store/settings.go`
- 修改：`internal/server/settings_api.go`

- [ ] **步骤 1：编写失败的 API 测试**

```go
func TestWecomNotificationSettingsPreserveWebhookURLs(t *testing.T) {
    test := newSettingsAPITest(t)
    body := `{"wecom":{"enabled":true,"urls":["https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=secret"],"payload_template":"{\\\"msgtype\\\":\\\"text\\\",\\\"text\\\":{\\\"content\\\":{{message}}}}"}}`
    recorder := test.request(t, http.MethodPut, "/api/settings/notifications", body)
    if recorder.Code != http.StatusOK { t.Fatalf("status = %d", recorder.Code) }
    if bytes.Contains(recorder.Body.Bytes(), []byte("key=secret")) { t.Fatal("response leaked webhook URL") }
    stored, err := test.database.NotificationSetting(context.Background(), "wecom")
    if err != nil || !bytes.Contains(stored.Config, []byte("key=secret")) { t.Fatalf("stored = %s, err = %v", stored.Config, err) }
}

func TestWecomNotificationSettingsRejectMalformedTemplate(t *testing.T) {
    test := newSettingsAPITest(t)
    recorder := test.request(t, http.MethodPut, "/api/settings/notifications", `{"wecom":{"enabled":true,"urls":["https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=x"],"payload_template":"[]"}}`)
    if recorder.Code != http.StatusBadRequest { t.Fatalf("status = %d", recorder.Code) }
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/server -run 'TestWecomNotificationSettings' -count=1`

预期：FAIL，设置 API 返回 `invalid_notification_channel`。

- [ ] **步骤 3：实现 API 契约、保存和测试端点**

在 `notificationChannels` 中加入 `wecom`，在 `notificationFields` 中登记 `urls: strings` 和 `payload_template: wecom_template`。将 `urls` 加入 `DefaultNotificationSensitiveFields("wecom")`。在字段验证中对 `wecom_template` 调用 `renderWecomPayload`，以默认测试变量确认模板会生成对象；在 `validateNotificationTestConfig`、`handleNotificationTest` 和发送分支中支持 `wecom`。

```go
"wecom": {"urls": "strings", "payload_template": "wecom_template"},

case "wecom":
    return []string{"urls"}

case "wecom":
    err = sendWecomNotificationTest(r.Context(), resolved)
```

将上段 `payload_template` 的字段类型实现为 `wecom_template`，避免只按普通字符串检查：

```go
case "wecom_template":
    var template string
    if err := json.Unmarshal(raw, &template); err != nil || len(template) > 32768 {
        return fmt.Errorf("%s must be a template string", field)
    }
    _, err := renderWecomPayload(template, wecomTestValues(time.Unix(0, 0)))
    return err

case "wecom":
    if len(configStrings(config, "urls")) == 0 || configString(config, "payload_template") == "" {
        return errors.New("wecom.urls and wecom.payload_template are required")
    }
```

测试消息的变量必须为 `event: "test"`、`title: "vocat"`、`message: "vocat notification test"` 和当前 UTC RFC3339 时间；它应经过与生产消息完全相同的渲染和发送路径。

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/server -run 'TestWecomNotificationSettings|TestNotificationSettingsAlwaysReturns' -count=1`

预期：PASS，GET/PUT 响应不会泄露 `key`，但数据库保留原 URL。

- [ ] **步骤 5：提交本任务**

运行：`git add internal/server/settings_api.go internal/server/settings_api_test.go internal/store/settings.go && git commit -m "feat: configure WeCom notifications"`

预期：创建设置 API 与敏感配置提交；作者身份未配置时遵循任务 1 的处理方式。

### 任务 3：接入短信与自动任务分发

**文件：**
- 修改：`internal/server/sms_notifications_test.go`
- 修改：`internal/server/sms_notifications.go`
- 修改：`internal/server/automatic_task_notifications.go`

- [ ] **步骤 1：编写失败的事件变量测试**

```go
func TestWecomSMSValuesIncludeRenderedSMSFields(t *testing.T) {
    message := smsNotification{DeviceID: "device-1", DeviceName: "客厅", DeviceLabel: "EC20", Number: "+447386", Time: time.Unix(1700000000, 0), Content: "hello"}
    values := wecomSMSValues(message)
    if values["event"] != "sms.received" || values["content"] != "hello" || values["device_label"] != "EC20" {
        t.Fatalf("values = %#v", values)
    }
}

func TestWecomAutomaticTaskValuesLeaveSMSFieldsEmpty(t *testing.T) {
    values := wecomAutomaticTaskValues(automaticTaskNotification{Title: "自动任务执行成功", Text: "任务已完成", Time: time.Unix(1700000000, 0)})
    if values["event"] != "automatic_task.completed" || values["message"] != "任务已完成" || values["number"] != "" {
        t.Fatalf("values = %#v", values)
    }
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/server -run 'TestWecomSMSValues|TestWecomAutomaticTaskValues' -count=1`

预期：FAIL，两个事件变量构建函数未定义。

- [ ] **步骤 3：实现分发接入**

在企业微信模块中实现 `wecomSMSValues` 和 `wecomAutomaticTaskValues`，填充全部已声明变量，短信专属字段在自动任务事件中设为空字符串。然后将 `wecom` 加入以下分发列表与 switch：

```go
var smsOnlyNotificationChannels = []string{"bark", "email", "pushplus", "webhook", "wecom"}

case "wecom":
    return sendWecomNotification(ctx, config, wecomSMSValues(message))
```

```go
channels := []string{"telegram", "bark", "email", "pushplus", "webhook", "wecom"}
for _, channel := range channels {
    setting, err := s.store.NotificationSetting(ctx, channel)
    if errors.Is(err, store.ErrNotFound) || (err == nil && !setting.Enabled) { continue }
    if err != nil { s.logger.Warn("read automatic task notification setting", "channel", channel, "error", err); continue }
    var config map[string]any
    if err := json.Unmarshal(setting.Config, &config); err != nil { s.logger.Warn("decode automatic task notification setting", "channel", channel, "error", err); continue }
    if err := sendAutomaticTaskNotification(ctx, channel, config, notification); err != nil { s.logger.Warn("send automatic task notification", "channel", channel, "task_id", task.ID, "error", err) }
}

case "wecom":
    return sendWecomNotification(ctx, config, wecomAutomaticTaskValues(message))
```

保持既有游标、错误限流日志和其他通道的行为不变。

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/server -run 'TestWecomSMSValues|TestWecomAutomaticTaskValues|TestValidateSMSNotificationConfig' -count=1`

预期：PASS，`validateSMSNotificationConfig` 也接受包含有效 URL 和模板的 `wecom` 配置。

- [ ] **步骤 5：提交本任务**

运行：`git add internal/server/wecom_notification.go internal/server/sms_notifications.go internal/server/sms_notifications_test.go internal/server/automatic_task_notifications.go && git commit -m "feat: dispatch WeCom notifications"`

预期：创建两类事件分发接入提交；作者身份未配置时遵循任务 1 的处理方式。

### 任务 4：企业微信配置界面

**文件：**
- 修改：`web/src/types.ts`
- 修改：`web/src/components/settings/model.ts`
- 修改：`web/src/components/settings/PushTabs.tsx`
- 修改：`web/src/pages/SettingsPage.tsx`

- [ ] **步骤 1：扩展前端类型和表单映射**

在 `NotificationSettings` 与 `NotifyForms` 中增加 `wecom`。新增以下表单类型和默认请求体；URL 数组保持一项一个输入行的既有 `UrlListEditor` 约定。

```ts
export interface WecomForm {
  enabled: boolean;
  urls: string[];
  payloadTemplate: string;
}

const DEFAULT_WECOM_PAYLOAD_TEMPLATE = `{
  "msgtype": "text",
  "text": { "content": {{message}} }
}`;
```

`formsFromNotifications` 读取 `payload_template`，`buildNotificationsPayload` 输出 `payload_template`，测试请求则修剪并移除空 URL。

- [ ] **步骤 2：实现企业微信页签与测试请求**

在 `PushTabs.tsx` 增加 `WecomTab`，显示启用开关、`UrlListEditor`、JSON `Textarea` 和变量说明。URL 列表文案必须明确“每个 Webhook URL 单独一行，点击添加 URL 增加”，不得提示使用分隔符。

```tsx
<Field label={t("JSON 请求体模板")} hint={<span>变量必须作为 JSON 值使用，例如 <code>{'{{message}}'}</code>。</span>}>
  <Textarea value={value.payloadTemplate} onChange={(event) => onChange({ payloadTemplate: event.target.value })} disabled={off} rows={12} />
</Field>
```

在 `SettingsPage.tsx` 增加 `testingWecom`、`onTestWecom`、企业微信页签与组件渲染。测试请求使用 `POST /settings/notifications/wecom/test` 和企业微信表单 payload；成功与失败消息沿用现有通知测试模式。

- [ ] **步骤 3：运行前端构建验证**

运行：`npm run build`

工作目录：`web`

预期：Vite 类型检查与生产构建均以退出码 0 完成。

- [ ] **步骤 4：提交本任务**

运行：`git add web/src/types.ts web/src/components/settings/model.ts web/src/components/settings/PushTabs.tsx web/src/pages/SettingsPage.tsx && git commit -m "feat: add WeCom notification settings"`

预期：创建企业微信设置 UI 提交；作者身份未配置时遵循任务 1 的处理方式。

### 任务 5：完整验证

**文件：**
- 修改：`internal/server/wecom_notification.go`
- 修改：`internal/server/wecom_notification_test.go`
- 修改：`internal/server/settings_api.go`
- 修改：`internal/server/settings_api_test.go`
- 修改：`internal/store/settings.go`
- 修改：`internal/server/sms_notifications.go`
- 修改：`internal/server/sms_notifications_test.go`
- 修改：`internal/server/automatic_task_notifications.go`
- 修改：`web/src/types.ts`
- 修改：`web/src/components/settings/model.ts`
- 修改：`web/src/components/settings/PushTabs.tsx`
- 修改：`web/src/pages/SettingsPage.tsx`

- [ ] **步骤 1：格式化 Go 代码**

运行：`gofmt -w internal/server/wecom_notification.go internal/server/wecom_notification_test.go internal/server/settings_api.go internal/server/settings_api_test.go internal/server/sms_notifications.go internal/server/sms_notifications_test.go internal/server/automatic_task_notifications.go internal/store/settings.go`

预期：所有修改的 Go 文件采用项目标准格式。

- [ ] **步骤 2：运行前端生产构建**

运行：`npm run build`

工作目录：`web`

预期：退出码 0，并生成 `web/dist` 供 Go 的嵌入资源使用。

- [ ] **步骤 3：运行后端回归测试**

运行：`go test ./...`

预期：所有目标包通过，无失败测试；`cmd/vocat` 和 `web` 包从步骤 2 生成的 `web/dist` 读取嵌入资源。

- [ ] **步骤 4：检查最终变更**

运行：`git diff --check && git status --short`

预期：无空白错误；变更仅限企业微信通知、其测试与设计/计划文档。
