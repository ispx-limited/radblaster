package radius

// Code is the RADIUS packet type carried in the first header octet.
type Code uint8

// Packet codes. Values are assigned by IANA; see RFC 2865, RFC 2866 and
// RFC 5176.
const (
	CodeAccessRequest      Code = 1
	CodeAccessAccept       Code = 2
	CodeAccessReject       Code = 3
	CodeAccountingRequest  Code = 4
	CodeAccountingResponse Code = 5
	CodeAccessChallenge    Code = 11
	CodeStatusServer       Code = 12
	CodeDisconnectRequest  Code = 40
	CodeDisconnectACK      Code = 41
	CodeDisconnectNAK      Code = 42
	CodeCoARequest         Code = 43
	CodeCoAACK             Code = 44
	CodeCoANAK             Code = 45
)

var codeNames = map[Code]string{
	CodeAccessRequest:      "Access-Request",
	CodeAccessAccept:       "Access-Accept",
	CodeAccessReject:       "Access-Reject",
	CodeAccountingRequest:  "Accounting-Request",
	CodeAccountingResponse: "Accounting-Response",
	CodeAccessChallenge:    "Access-Challenge",
	CodeStatusServer:       "Status-Server",
	CodeDisconnectRequest:  "Disconnect-Request",
	CodeDisconnectACK:      "Disconnect-ACK",
	CodeDisconnectNAK:      "Disconnect-NAK",
	CodeCoARequest:         "CoA-Request",
	CodeCoAACK:             "CoA-ACK",
	CodeCoANAK:             "CoA-NAK",
}

func (c Code) String() string {
	if n, ok := codeNames[c]; ok {
		return n
	}
	return "Code(" + itoa(uint32(c)) + ")"
}

// isRequest reports whether c is a client-originated code whose authenticator
// this package knows how to build.
func (c Code) isRequest() bool {
	switch c {
	case CodeAccessRequest, CodeAccountingRequest, CodeStatusServer,
		CodeDisconnectRequest, CodeCoARequest:
		return true
	}
	return false
}

// randomAuthenticator reports whether c carries an unpredictable Request
// Authenticator (RFC 2865 section 3) rather than one derived from the packet
// contents.
func (c Code) randomAuthenticator() bool {
	return c == CodeAccessRequest || c == CodeStatusServer
}

// itoa formats v without pulling in strconv, keeping this file dependency free.
func itoa(v uint32) string {
	if v == 0 {
		return "0"
	}
	var b [10]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
