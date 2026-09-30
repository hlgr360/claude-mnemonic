//go:build windows && amd64

package embedding

import (
	_ "embed"
)

//go:embed assets/lib/windows-amd64/onnxruntime.dll.gz
var onnxRuntimeLib []byte

const onnxRuntimeLibName = "onnxruntime.dll"
