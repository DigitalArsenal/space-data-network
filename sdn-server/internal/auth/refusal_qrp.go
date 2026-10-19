package auth

import (
	"net/http"
	"strconv"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/QRP"
	flatbuffers "github.com/google/flatbuffers/go"
)

// qrpStreamContentType is api.StreamContentType; auth cannot import api.
const qrpStreamContentType = "application/vnd.sdn.flatbuffers.stream"

// $QRP ordinals (QRP.fbs): KIND Error, STATUS Error.
const (
	qrpKindError   int8 = 2
	qrpStatusError int8 = 4
)

func qrpEnum[T ~int8](_ map[string]T, n int8) T { return T(n) }

// refusalFrame is one size-prefixed $QRP{KIND=Error, STATUS=Error} frame.
func refusalFrame(status int, code, message string) []byte {
	b := flatbuffers.NewBuilder(128)
	codeOff := b.CreateString(code)
	msgOff := b.CreateString(message)
	QRP.QRPStart(b)
	QRP.QRPAddKIND(b, qrpEnum(QRP.EnumValuesqrpKind, qrpKindError))
	QRP.QRPAddSTATUS(b, qrpEnum(QRP.EnumValuesqrpStatus, qrpStatusError))
	QRP.QRPAddERROR_CODE(b, codeOff)
	QRP.QRPAddMESSAGE(b, msgOff)
	QRP.QRPAddHTTP_STATUS(b, uint16(status))
	QRP.QRPAddGENERATED_AT_MS(b, time.Now().UnixMilli())
	QRP.FinishSizePrefixedQRPBuffer(b, QRP.QRPEnd(b))
	return b.FinishedBytes()
}

// writeQRPRefusal answers a data-route refusal as exactly one $QRP Error
// frame.
func writeQRPRefusal(w http.ResponseWriter, status int, body errorResponse) {
	frame := refusalFrame(status, body.Code, body.Message)
	h := w.Header()
	h.Set("Content-Type", qrpStreamContentType)
	h.Set("X-SDN-Stream-Format", "flatsql-size-prefixed-le-u32")
	h.Set("X-SDN-Record-Count", "1")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.Itoa(len(frame)))
	w.WriteHeader(status)
	_, _ = w.Write(frame)
}
