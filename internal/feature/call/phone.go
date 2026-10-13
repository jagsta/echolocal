package call

// Card 6 (D-DI-8, specs/phone-dropin-webrtc.md): phone-peer acceptance parity.
//
// A phone/web caller is NOT a discovered peer: it posts POST /call/offer
// directly with From = "phone:<display-name>" in the envelope body. The
// callee must surface that name in its ring announcement/peer lookup exactly
// like a Dot-to-Dot call — same Ringing state, same 3s floor, chime + LED,
// button answer (Action=AnswerAudio) / hang-up (VolumeDown), busy handling
// — without any discovery dependence for phone peers.
//
// The upstream offered() handler already synthesizes an ad-hoc peer when
// Peers.Find misses; the seam here makes the identity CONVENTION explicit
// ("phone:<name>" ids, display name derived from the id, Touch no-op so
// phone peers are never persisted into discovery state).
const PhoneSourcePrefix = "phone:"

// IsPhoneSource reports whether a caller id is a phone/web peer id
// ("phone:<name>", per D-DI-8c).
func IsPhoneSource(id string) bool { return len(id) >= len(PhoneSourcePrefix) && id[:len(PhoneSourcePrefix)] == PhoneSourcePrefix }

// PhoneDisplayName strips the "phone:" prefix for the callee's ring
// announcement ("phone:jag" announces as "jag").
func PhoneDisplayName(id string) string { return id[len(PhoneSourcePrefix):] }