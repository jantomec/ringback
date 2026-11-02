package main

/*
#cgo pkg-config: opus
#include <opus.h>

static int encoder_setup(OpusEncoder *e, int bitrate, int loss) {
	int r = opus_encoder_ctl(e, OPUS_SET_BITRATE(bitrate));
	if (r == OPUS_OK) r = opus_encoder_ctl(e, OPUS_SET_INBAND_FEC(1));
	if (r == OPUS_OK) r = opus_encoder_ctl(e, OPUS_SET_PACKET_LOSS_PERC(loss));
	return r;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

// The call's Opus on the hub, set up for weak signal, as the Python voice was (see "Data and
// weak signal" in docs/design.md):
//   - the voice goes out mono at 24 kbps, with Opus's forward error correction (FEC: a rougher
//     copy of each packet in the next), set for 10% loss;
//   - the answer to the phone's offer asks the phone for 24 kbps, only the band speech
//     recognition uses (it hears at 16 kHz), and FEC (phoneOpus);
//   - what you say goes through a jitter buffer that passes each packet on as soon as those
//     before it have come, and gives up on a missing one once 3 later ones have;
//   - a lost packet is rebuilt from the next one's FEC, or else concealed, before speech
//     recognition hears it.
const (
	voiceBitrate = 24000
	expectedLoss = 10 // percent: FEC needs some, and at 5% Opus left it out at 24 kbps
	waitPackets  = 3  // later packets after which a missing one is given up on
	hearingRate  = 16000
	speakingRate = 24000
	rtpRate      = 48000 // Opus's RTP clock, whatever the audio's rate
)

// phoneOpus is what the phone's encoder is asked for in the answer: Google's WebRTC sends mono
// at 32 kbps in Opus's full band and without FEC unless the answer says otherwise.
const phoneOpus = "minptime=10;useinbandfec=1;maxaveragebitrate=24000;maxplaybackrate=16000"

// opusEncoder makes the voice's packets: 20 ms of mono at 24 kHz each.
type opusEncoder struct{ e *C.OpusEncoder }

func newOpusEncoder() (*opusEncoder, error) {
	var err C.int
	e := C.opus_encoder_create(speakingRate, 1, C.OPUS_APPLICATION_VOIP, &err)
	if err != C.OPUS_OK {
		return nil, fmt.Errorf("opus_encoder_create: %d", int(err))
	}
	if r := C.encoder_setup(e, voiceBitrate, expectedLoss); r != C.OPUS_OK {
		C.opus_encoder_destroy(e)
		return nil, fmt.Errorf("opus encoder settings: %d", int(r))
	}
	return &opusEncoder{e}, nil
}

// encode one frame of samples (480 at 24 kHz for 20 ms).
func (o *opusEncoder) encode(pcm []int16) ([]byte, error) {
	out := make([]byte, 1500)
	n := C.opus_encode(o.e, (*C.opus_int16)(unsafe.Pointer(&pcm[0])), C.int(len(pcm)), (*C.uchar)(unsafe.Pointer(&out[0])), C.opus_int32(len(out)))
	if n < 0 {
		return nil, fmt.Errorf("opus_encode: %d", int(n))
	}
	return out[:n], nil
}

func (o *opusEncoder) close() { C.opus_encoder_destroy(o.e) }

// opusDecoder decodes what you say straight to 16 kHz mono, which speech recognition takes,
// and makes up for lost packets: a gap in the timestamps is filled from the next packet's
// FEC, and where that has none, or for all but the last of several lost packets, by Opus's
// concealment. A gap over a second is left out.
type opusDecoder struct {
	d    *C.OpusDecoder
	next uint32 // the RTP timestamp the next packet should have
	have bool
}

func newOpusDecoder() (*opusDecoder, error) {
	var err C.int
	d := C.opus_decoder_create(hearingRate, 1, &err)
	if err != C.OPUS_OK {
		return nil, fmt.Errorf("opus_decoder_create: %d", int(err))
	}
	return &opusDecoder{d: d}, nil
}

func (o *opusDecoder) close() { C.opus_decoder_destroy(o.d) }

// decode one packet with its RTP timestamp; returns the samples to hear, with any made up
// for a gap before them.
func (o *opusDecoder) decode(data []byte, timestamp uint32) ([]int16, error) {
	if len(data) == 0 {
		return nil, nil
	}
	n := C.opus_packet_get_nb_samples((*C.uchar)(unsafe.Pointer(&data[0])), C.opus_int32(len(data)), rtpRate)
	if n <= 0 {
		return nil, errors.New("not an Opus packet")
	}
	var out []int16
	if o.have {
		lost := int32(timestamp - o.next) // in RTP units, 48 kHz
		lost -= lost % 120                // Opus makes up whole 2.5 ms
		if lost > 0 && lost <= rtpRate {
			if pcm := o.run(data, int(lost)/3, true); pcm != nil {
				out = append(out, pcm...)
			}
		}
	}
	if pcm := o.run(data, int(n)/3, false); pcm != nil {
		out = append(out, pcm...)
	}
	o.next, o.have = timestamp+uint32(n), true
	return out, nil
}

func (o *opusDecoder) run(data []byte, samples int, fec bool) []int16 {
	pcm := make([]int16, samples)
	flag := C.int(0)
	if fec {
		flag = 1
	}
	done := C.opus_decode(o.d, (*C.uchar)(unsafe.Pointer(&data[0])), C.opus_int32(len(data)), (*C.opus_int16)(unsafe.Pointer(&pcm[0])), C.int(samples), flag)
	if done <= 0 {
		return nil
	}
	return pcm[:done]
}

// jitterBuffer is for a listener rather than a speaker: speech recognition takes audio as it
// comes, so nothing is held back but for a missing packet, and that only until waitPackets
// later ones have come. A packet that comes after that is dropped; its FEC came in the next.
type jitterBuffer struct {
	next uint16
	have bool
	held map[uint16]rtpPacket
}

type rtpPacket struct {
	seq       uint16
	timestamp uint32
	payload   []byte
}

func newJitterBuffer() *jitterBuffer { return &jitterBuffer{held: map[uint16]rtpPacket{}} }

// add one packet; returns those to decode now, in order.
func (j *jitterBuffer) add(p rtpPacket) []rtpPacket {
	if !j.have {
		j.next, j.have = p.seq, true
	}
	ahead := p.seq - j.next
	if ahead >= 1<<15 {
		if uint16(0)-ahead <= 100 {
			return nil // late, or a repeat
		}
		clear(j.held) // far behind: the stream started over
		j.next = p.seq
	}
	j.held[p.seq] = p
	var out []rtpPacket
	for len(j.held) > 0 {
		if q, ok := j.held[j.next]; ok {
			out = append(out, q)
			delete(j.held, j.next)
			j.next++
		} else if len(j.held) >= waitPackets {
			best, bestAhead := j.next, uint16(0xffff)
			for s := range j.held {
				if a := s - j.next; a < bestAhead {
					best, bestAhead = s, a
				}
			}
			j.next = best
		} else {
			break
		}
	}
	return out
}
