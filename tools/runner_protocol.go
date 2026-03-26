package tools

import (
	"xbot/internal/runnerproto"
)

// Re-export all protocol types and constants from the shared package.
// This preserves backward compatibility for existing code that imports from tools.

// === WebSocket Protocol Constants ===

const (
	ProtoExec      = runnerproto.ProtoExec
	ProtoReadFile  = runnerproto.ProtoReadFile
	ProtoWriteFile = runnerproto.ProtoWriteFile
	ProtoStat      = runnerproto.ProtoStat
	ProtoReadDir   = runnerproto.ProtoReadDir
	ProtoMkdirAll  = runnerproto.ProtoMkdirAll
	ProtoRemove    = runnerproto.ProtoRemove
	ProtoRemoveAll = runnerproto.ProtoRemoveAll

	ProtoExecResult  = runnerproto.ProtoExecResult
	ProtoFileContent = runnerproto.ProtoFileContent
	ProtoFileInfo    = runnerproto.ProtoFileInfo
	ProtoDirEntries  = runnerproto.ProtoDirEntries
	ProtoError       = runnerproto.ProtoError
	ProtoOK          = runnerproto.ProtoOK
)

// === WebSocket Protocol Types ===

type RunnerMessage = runnerproto.RunnerMessage
type RegisterRequest = runnerproto.RegisterRequest
type ExecRequest = runnerproto.ExecRequest
type ExecResultResponse = runnerproto.ExecResultResponse
type ReadFileRequest = runnerproto.ReadFileRequest
type FileContentResponse = runnerproto.FileContentResponse
type WriteFileRequest = runnerproto.WriteFileRequest
type StatRequest = runnerproto.StatRequest
type StatResponse = runnerproto.StatResponse
type ReadDirRequest = runnerproto.ReadDirRequest
type DirEntryResponse = runnerproto.DirEntryResponse
type DirEntriesResponse = runnerproto.DirEntriesResponse
type PathRequest = runnerproto.PathRequest
type ErrorResponse = runnerproto.ErrorResponse

// ProtoErrorCodes maps protocol error codes to Go errors.
var ProtoErrorCodes = runnerproto.ProtoErrorCodes

// ProtoErrorCode converts a Go error to a protocol error code.
var ProtoErrorCode = runnerproto.ProtoErrorCode

// wsFileThreshold is the size above which file transfer uses HTTP instead of WebSocket.
const wsFileThreshold = runnerproto.WsFileThreshold

// defaultRequestTimeout is the default timeout for non-exec operations.
const defaultRequestTimeout = runnerproto.DefaultRequestTimeout
