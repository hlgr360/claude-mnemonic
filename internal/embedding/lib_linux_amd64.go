//go:build linux && amd64

package embedding

import (
	_ "embed"
)

//go:embed assets/lib/linux-amd64/libonnxruntime.so.gz
var onnxRuntimeLib []byte

//go:embed assets/lib/linux-amd64/libonnxruntime_providers_shared.so.gz
var onnxRuntimeProvidersLib []byte

const onnxRuntimeLibName = "libonnxruntime.so"
const onnxRuntimeProvidersLibName = "libonnxruntime_providers_shared.so"
