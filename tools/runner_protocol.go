package tools

import (
	"xbot/internal/runnerproto"
)

// Re-export all protocol types and constants from the shared package.
// This preserves backward compatibility for existing code that imports from tools.

// === WebSocket Protocol Constants ===

const (
	ProtoExec         = runnerproto.ProtoExec
	ProtoReadFile     = runnerproto.ProtoReadFile
	ProtoWriteFile    = runnerproto.ProtoWriteFile
	ProtoStat         = runnerproto.ProtoStat
	ProtoReadDir      = runnerproto.ProtoReadDir
	ProtoMkdirAll     = runnerproto.ProtoMkdirAll
	ProtoRemove       = runnerproto.ProtoRemove
	ProtoRemoveAll    = runnerproto.ProtoRemoveAll
	ProtoDownloadFile = runnerproto.ProtoDownloadFile

	ProtoExecResult  = runnerproto.ProtoExecResult
	ProtoFileContent = runnerproto.ProtoFileContent
	ProtoFileInfo    = runnerproto.ProtoFileInfo
	ProtoDirEntries  = runnerproto.ProtoDirEntries
	ProtoError       = runnerproto.ProtoError
	ProtoOK          = runnerproto.ProtoOK

	ProtoStdioStart = runnerproto.ProtoStdioStart
	ProtoStdioWrite = runnerproto.ProtoStdioWrite
	ProtoStdioClose = runnerproto.ProtoStdioClose
	ProtoStdioData  = runnerproto.ProtoStdioData
	ProtoStdioExit  = runnerproto.ProtoStdioExit
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
type DownloadFileRequest = runnerproto.DownloadFileRequest
type DownloadFileResponse = runnerproto.DownloadFileResponse
type ErrorResponse = runnerproto.ErrorResponse

type StdioStartRequest = runnerproto.StdioStartRequest
type StdioStartResponse = runnerproto.StdioStartResponse
type StdioWriteRequest = runnerproto.StdioWriteRequest
type StdioCloseRequest = runnerproto.StdioCloseRequest
type StdioDataMessage = runnerproto.StdioDataMessage
type StdioExitMessage = runnerproto.StdioExitMessage

// ProtoErrorCodes maps protocol error codes to Go errors.
var ProtoErrorCodes = runnerproto.ProtoErrorCodes

// ProtoErrorCode converts a Go error to a protocol error code.
var ProtoErrorCode = runnerproto.ProtoErrorCode

// defaultRequestTimeout is the default timeout for non-exec operations.
const defaultRequestTimeout = runnerproto.DefaultRequestTimeout
