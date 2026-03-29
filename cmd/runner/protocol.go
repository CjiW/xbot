package main

// Protocol types are imported from xbot/internal/runnerproto.
// Both the server (tools/remote_sandbox.go) and the runner share the same
// protocol definitions — no duplication, no sync issues.

import (
	"xbot/internal/runnerproto"
)

// Re-export all protocol types and constants for use within cmd/runner.

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
)

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

var ProtoErrorCodes = runnerproto.ProtoErrorCodes

// makeResponse creates a RunnerMessage with the given type and body.
func makeResponse(id, respType string, body interface{}) *RunnerMessage {
	return runnerproto.MakeResponse(id, respType, body)
}

// makeError creates an error RunnerMessage.
func makeError(id string, code, message string) *RunnerMessage {
	return runnerproto.MakeError(id, code, message)
}

// makeOK creates an OK RunnerMessage.
func makeOK(id string) *RunnerMessage {
	return runnerproto.MakeOK(id)
}

// protoErrorCode converts a Go error to a protocol error code.
func protoErrorCode(err error) string {
	return runnerproto.ProtoErrorCode(err)
}

const DefaultRequestTimeout = runnerproto.DefaultRequestTimeout
