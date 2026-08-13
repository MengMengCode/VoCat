package qmi

import (
	"errors"
	"fmt"
)

// ============================================================================
// QMI Error Types / QMI 错误类型
// 提供结构化的错误信息，方便调用方进行错误处理和判断
// ============================================================================

// QMIError represents a QMI protocol error / QMIError 表示 QMI 协议错误
type QMIError struct {
	Service   uint8  // QMI service type / QMI 服务类型
	MessageID uint16 // Message ID that caused the error / 导致错误的消息 ID
	Result    uint16 // QMI result code / QMI 结果码
	ErrorCode uint16 // QMI error code / QMI 错误码
}

func (e *QMIError) Error() string {
	return fmt.Sprintf("QMI error: service=0x%02x msg=0x%04x result=0x%04x error=0x%04x",
		e.Service, e.MessageID, e.Result, e.ErrorCode)
}

// IsQMIError 0x0030s if err is a QMI protocol error / IsQMIError 检查是否为 QMI 协议错误
func IsQMIError(err error) bool {
	var qe *QMIError
	return errors.As(err, &qe)
}

// GetQMIError extracts QMIError from err / GetQMIError 从 err 中提取 QMIError
func GetQMIError(err error) *QMIError {
	var qe *QMIError
	if errors.As(err, &qe) {
		return qe
	}
	return nil
}

// Common QMI error codes / 常见 QMI 错误码
const (
	QMIErrNone         uint16 = 0x0000 // Success / 成功
	QMIErrMalformedMsg uint16 = 0x0001 // Malformed message / 消息格式错误
	QMIErrNoMemory     uint16 = 0x0002 // No memory / 内存不足
	QMIErrInternal     uint16 = 0x0003 // Internal error / 内部错误
	QMIErrInvalidID    uint16 = 0x0029 // Invalid client ID / 无效客户端 ID
	QMIErrNoEffect     uint16 = 0x001A // No effect / 无效果
	// Qualcomm's Gobi/QMI error enumeration assigns INVALID_ARG to 48
	// (0x0030). 0x0004 is ABORTED in that enumeration; keep the old value
	// available under an explicit name for devices that use the legacy map.
	QMIErrInvalidArg             uint16 = 0x0030 // Invalid argument / 无效参数
	QMIErrAborted                uint16 = 0x0004 // Aborted (legacy map) / 已中止（旧映射）
	QMIErrDeviceNotReady         uint16 = 0x0005 // Device not ready / 设备未就绪
	QMIErrNetworkNotReady        uint16 = 0x0006 // Network not ready / 网络未就绪
	QMIErrNoThresholds           uint16 = 0x0008 // No thresholds / 未设置阈值
	QMIErrCallFailed             uint16 = 0x000E // Call failed / 呼叫失败
	QMIErrOutOfCall              uint16 = 0x000F // Out of call / 未建立数据呼叫
	QMIErrPolicyMismatch         uint16 = 0x004A // Policy mismatch / 策略不匹配
	QMIErrInvalidProfile         uint16 = 0x0019 // Invalid profile / 无效配置文件
	QMIErrClientIDsExhausted     uint16 = 0x001F // Client IDs exhausted / 客户端 ID 耗尽
	QMIErrInvalidRegisterAction  uint16 = 0x0020 // Invalid register action / 无效驻网动作
	QMIErrInvalidQmiCmd          uint16 = 0x0047 // Invalid QMI command / 不支持的QMI命令
	QMIErrNotSupported           uint16 = 0x005E // Not supported / 不支持
	QMIErrOpDeviceUnsupported    uint16 = 0x0034 // Operation not supported by device (EC20 对 WMS 0x004A 的常见回应)
	QMIErrCardCallControlRefFail uint16 = 0x0060 // Card call control failed / 卡片 call-control 失败
)

// ============================================================================
// Connection Error / 连接错误
// ============================================================================

// ConnectionError represents a connection-level error / ConnectionError 表示连接级别错误
type ConnectionError struct {
	Phase   ConnectionPhase // Phase where error occurred / 错误发生阶段
	Cause   error           // Underlying cause / 底层原因
	Message string          // Human readable message / 可读消息
}

// ConnectionPhase indicates where in the connection process an error occurred
// ConnectionPhase 表示连接过程中错误发生的阶段
type ConnectionPhase int

const (
	PhaseDeviceOpen   ConnectionPhase = iota // Opening QMI device / 打开 QMI 设备
	PhaseClientAlloc                         // Allocating QMI client / 分配 QMI 客户端
	PhaseSIMCheck                            // Checking SIM status / 检查 SIM 状态
	PhaseRegistration                        // Network registration / 网络注册
	PhaseDialing                             // Establishing data call / 建立数据呼叫
	PhaseIPConfig                            // Configuring IP / 配置 IP
	PhaseConnected                           // While connected / 已连接状态
)

func (p ConnectionPhase) String() string {
	switch p {
	case PhaseDeviceOpen:
		return "DeviceOpen"
	case PhaseClientAlloc:
		return "ClientAlloc"
	case PhaseSIMCheck:
		return "SIMCheck"
	case PhaseRegistration:
		return "Registration"
	case PhaseDialing:
		return "Dialing"
	case PhaseIPConfig:
		return "IPConfig"
	case PhaseConnected:
		return "Connected"
	default:
		return "Unknown"
	}
}

func (e *ConnectionError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("connection error at %s: %s: %v", e.Phase, e.Message, e.Cause)
	}
	return fmt.Sprintf("connection error at %s: %s", e.Phase, e.Message)
}

func (e *ConnectionError) Unwrap() error {
	return e.Cause
}

// IsConnectionError 0x0030s if err is a connection error / IsConnectionError 检查是否为连接错误
func IsConnectionError(err error) bool {
	_, ok := err.(*ConnectionError)
	return ok
}

// NewConnectionError creates a new ConnectionError / NewConnectionError 创建新的连接错误
func NewConnectionError(phase ConnectionPhase, message string, cause error) *ConnectionError {
	return &ConnectionError{
		Phase:   phase,
		Message: message,
		Cause:   cause,
	}
}

// ============================================================================
// Device Error / 设备错误
// ============================================================================

// DeviceError represents a device-level error / DeviceError 表示设备级别错误
type DeviceError struct {
	DevicePath string // Device path / 设备路径
	Operation  string // Operation that failed / 失败的操作
	Cause      error  // Underlying cause / 底层原因
}

func (e *DeviceError) Error() string {
	return fmt.Sprintf("device %s: %s failed: %v", e.DevicePath, e.Operation, e.Cause)
}

func (e *DeviceError) Unwrap() error {
	return e.Cause
}

// IsDeviceError 0x0030s if err is a device error / IsDeviceError 检查是否为设备错误
func IsDeviceError(err error) bool {
	_, ok := err.(*DeviceError)
	return ok
}

// ============================================================================
// Timeout Error / 超时错误
// ============================================================================

// TimeoutError represents a timeout / TimeoutError 表示超时
type TimeoutError struct {
	Operation string // Operation that timed out / 超时的操作
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("timeout: %s", e.Operation)
}

// IsTimeoutError 0x0030s if err is a timeout error / IsTimeoutError 检查是否为超时错误
func IsTimeoutError(err error) bool {
	_, ok := err.(*TimeoutError)
	return ok
}
