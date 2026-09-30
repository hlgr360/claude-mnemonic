//go:build linux && arm64

package embedding

import (
	_ "embed"
)

//go:embed assets/lib/linux-arm64/libonnxruntime.so.gz
var onnxRuntimeLib []byte

//go:embed assets/lib/linux-arm64/libonnxruntime_providers_shared.so.gz
var onnxRuntimeProvidersLib []byte

const onnxRuntimeLibName = "libonnxruntime.so"
const onnxRuntimeProvidersLibName = "libonnxruntime_providers_shared.so"
