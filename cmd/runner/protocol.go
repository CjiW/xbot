package main

import (
	"xbot/internal/runnerproto"
)

// Re-export all protocol types from the shared package for convenience.
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

// Helper functions re-exported from shared package.
var (
	protoErrorCode = runnerproto.ProtoErrorCode
	makeResponse   = runnerproto.MakeResponse
	makeError      = runnerproto.MakeError
	makeOK         = runnerproto.MakeOK
)
