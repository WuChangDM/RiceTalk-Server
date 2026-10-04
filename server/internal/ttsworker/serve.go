// Package ttsworker: serve.go
//
// worker 进程的主循环：stdin 逐行读请求 → 引擎执行 → stdout 逐行写响应。
// 与引擎实现解耦（Engine 接口），使协议层可在无 cgo 环境下用桩引擎测试。
package ttsworker

import (
	"bufio"
	"errors"
	"io"
	"log"
	"strings"
)

// Engine 是 worker 进程持有的 TTS 引擎抽象。
// cgo 构建下由 sherpa-onnx 实现（engine_cgo.go）；无 cgo 构建下是 stub
//（engine_nocgo.go，所有请求返回 "TTS engine requires CGO"）。
type Engine interface {
	// Info 返回引擎元数据（info 握手）。引擎未就绪时返回非 nil error。
	Info() (sampleRate int32, numSpeakers int32, err error)
	// Synthesize 合成文本并写出 WAV 到 wavPath（主进程指定路径）。
	Synthesize(text, wavPath string, speed float64, sid int32) (sampleRate int32, err error)
	// Close 释放引擎资源（worker 退出前调用）。
	Close()
}

// ServeEngine 是 worker 进程主循环：读 stdin 行分隔 JSON 请求，写 stdout
// 行分隔 JSON 响应。stdin EOF 时优雅返回 nil（退出码 0）。
//
// 注意：cgo 引擎（sherpa-onnx）在解析损坏模型时可能在任意调用点 SIGABRT——
// 这正是引擎必须活在独立进程的根本原因，本循环不做（也无法做）recover。
func ServeEngine(eng Engine, in io.Reader, out io.Writer, logger *log.Logger) error {
	if logger == nil {
		logger = log.Default()
	}
	br := bufio.NewReaderSize(in, 64*1024)
	bw := bufio.NewWriter(out)
	for {
		line, rerr := br.ReadBytes('\n')
		if trimmed := strings.TrimSpace(string(line)); trimmed != "" {
			resp := handleLine(eng, []byte(trimmed), logger)
			encoded, encErr := EncodeResponse(resp)
			if encErr != nil {
				logger.Printf("[tts-worker] encode response: %v", encErr)
			} else if _, werr := bw.Write(encoded); werr != nil {
				return werr // stdout 断裂：主进程已不在，退出
			}
			if werr := bw.Flush(); werr != nil {
				return werr
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return nil // stdin EOF：优雅退出
			}
			return rerr
		}
	}
}

// handleLine 处理单行请求并产出响应。非法请求回 ok:false（不终止循环，
// 保持 worker 与主进程的通道存活）。
func handleLine(eng Engine, line []byte, logger *log.Logger) Response {
	req, err := DecodeRequest(line)
	if err != nil {
		logger.Printf("[tts-worker] bad request: %v", err)
		return Response{ID: "", OK: false, Error: err.Error()}
	}
	switch req.Op {
	case "", OpSynth:
		if req.Text == "" {
			return Response{ID: req.ID, OK: false, Error: "empty text"}
		}
		if req.WavPath == "" {
			return Response{ID: req.ID, OK: false, Error: "missing wavPath"}
		}
		speed := req.Speed
		if speed <= 0 {
			speed = 1.0
		}
		sampleRate, err := eng.Synthesize(req.Text, req.WavPath, speed, req.Sid)
		if err != nil {
			logger.Printf("[tts-worker] synth failed (id=%s): %v", req.ID, err)
			return Response{ID: req.ID, OK: false, Error: err.Error()}
		}
		return Response{ID: req.ID, OK: true, WavPath: req.WavPath, SampleRate: sampleRate}
	case OpInfo:
		sampleRate, numSpeakers, err := eng.Info()
		if err != nil {
			logger.Printf("[tts-worker] info failed (id=%s): %v", req.ID, err)
			return Response{ID: req.ID, OK: false, Error: err.Error()}
		}
		return Response{ID: req.ID, OK: true, SampleRate: sampleRate, NumSpeakers: numSpeakers}
	default:
		return Response{ID: req.ID, OK: false, Error: "unknown op: " + req.Op}
	}
}
