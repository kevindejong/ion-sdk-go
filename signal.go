package engine

import (
	"context"
	"encoding/json"
	"io"
	"sync"

	log "github.com/pion/ion-log"
	pb "github.com/pion/ion-sfu/cmd/signal/grpc/proto"
	"github.com/pion/webrtc/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Signal is a wrapper of grpc
type Signal struct {
	client pb.SFUClient
	stream pb.SFU_SignalClient

	OnNegotiate    func(webrtc.SessionDescription) error
	OnTrickle      func(candidate webrtc.ICECandidateInit, target int)
	OnSetRemoteSDP func(webrtc.SessionDescription) error

	ctx    context.Context
	cancel context.CancelFunc
	sync.Mutex
}

// NewSignal create a grpc signaler
func NewSignal(addr string) *Signal {
	s := &Signal{}
	log.Infof("Connecting to sfu: %s", addr)
	// Set up a connection to the sfu server.
	conn, err := grpc.Dial(addr, grpc.WithInsecure(), grpc.WithBlock())
	if err != nil {
		log.Errorf("did not connect: %v", err)
		return nil
	}

	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.client = pb.NewSFUClient(conn)
	s.stream, err = s.client.Signal(s.ctx)
	if err != nil {
		log.Errorf("err=%v", err)
	}
	go s.onSignalHandle()
	return s
}

func (s *Signal) onSignalHandle() {
	for {
		//only one goroutine for recving from stream, no need to lock
		res, err := s.stream.Recv()
		if err != nil {
			if err == io.EOF {
				log.Infof("WebRTC Transport Closed")
				err = s.stream.CloseSend()
				if err != nil {
					log.Errorf("error sending close: %s", err)
				}
				return
			}

			errStatus, _ := status.FromError(err)
			if errStatus.Code() == codes.Canceled {
				err = s.stream.CloseSend()
				if err != nil {
					log.Errorf("error sending close: %s", err)
				}
				return
			}

			log.Errorf("Error receiving signal response: %v", err)
			return
		}

		switch payload := res.Payload.(type) {
		case *pb.SignalReply_Join:
			// Set the remote SessionDescription
			log.Infof("[join] got answer: %s", payload.Join.Description)

			var sdp webrtc.SessionDescription
			err := json.Unmarshal(payload.Join.Description, &sdp)
			if err != nil {
				log.Errorf("[join] sdp unmarshal error: %v", err)
				return
			}

			if err = s.OnSetRemoteSDP(sdp); err != nil {
				log.Errorf("[join] s.OnSetRemoteSDP error %s", err)
				return
			}
		case *pb.SignalReply_Description:
			var sdp webrtc.SessionDescription
			err := json.Unmarshal(payload.Description, &sdp)
			if err != nil {
				log.Errorf("[description] sdp unmarshal error: %v", err)
				return
			}
			if sdp.Type == webrtc.SDPTypeOffer {
				log.Infof("[description] got offer call s.OnNegotiate sdp=%+v", sdp)
				err := s.OnNegotiate(sdp)
				if err != nil {
					log.Errorf("err=%v", err)
				}
			} else if sdp.Type == webrtc.SDPTypeAnswer {
				log.Infof("[description] got answer call s.OnSetRemoteSDP sdp=%+v", sdp)
				err = s.OnSetRemoteSDP(sdp)
				if err != nil {
					log.Errorf("[description] s.OnSetRemoteSDP err=%s", err)
				}
			}
		case *pb.SignalReply_Trickle:
			var candidate webrtc.ICECandidateInit
			_ = json.Unmarshal([]byte(payload.Trickle.Init), &candidate)
			log.Infof("[trickle] type=%v candidate=%v", payload.Trickle.Target, candidate)
			s.OnTrickle(candidate, int(payload.Trickle.Target))
		default:
			// log.Errorf("Unknow signal type!!!!%v", payload)
		}
	}
}

func (s *Signal) Join(sid string, offer webrtc.SessionDescription) error {
	log.Infof("[Signal.Join] sid=%v offer=%v", sid, offer)
	marshalled, err := json.Marshal(offer)
	if err != nil {
		return err
	}
	s.Lock()
	err = s.stream.Send(
		&pb.SignalRequest{
			Payload: &pb.SignalRequest_Join{
				Join: &pb.JoinRequest{
					Sid:         sid,
					Description: marshalled,
				},
			},
		},
	)
	s.Unlock()
	if err != nil {
		log.Errorf("err=%v", err)
	}
	return err
}

func (s *Signal) Trickle(candidate *webrtc.ICECandidate, target int) {
	log.Infof("[Signal.Trickle] candidate=%v target=%v", candidate, target)
	bytes, err := json.Marshal(candidate.ToJSON())
	if err != nil {
		log.Errorf("err=%v", err)
		return
	}
	s.Lock()
	err = s.stream.Send(&pb.SignalRequest{
		Payload: &pb.SignalRequest_Trickle{
			Trickle: &pb.Trickle{
				Init:   string(bytes),
				Target: pb.Trickle_Target(target),
			},
		},
	})
	s.Unlock()
	if err != nil {
		log.Errorf("err=%v", err)
	}
}

func (s *Signal) Offer(sdp webrtc.SessionDescription) {
	log.Infof("[Signal.Offer] sdp=%v", sdp)
	marshalled, err := json.Marshal(sdp)
	if err != nil {
		log.Errorf("err=%v", err)
		return
	}

	s.Lock()
	err = s.stream.Send(
		&pb.SignalRequest{
			Payload: &pb.SignalRequest_Description{
				Description: marshalled,
			},
		},
	)
	s.Unlock()
	if err != nil {
		log.Errorf("err=%v", err)
	}
}

func (s *Signal) Answer(sdp webrtc.SessionDescription) {
	log.Infof("[Signal.Answer] sdp=%v", sdp)
	marshalled, err := json.Marshal(sdp)
	if err != nil {
		log.Errorf("err=%v", err)
		return
	}

	s.Lock()
	err = s.stream.Send(
		&pb.SignalRequest{
			Payload: &pb.SignalRequest_Description{
				Description: marshalled,
			},
		},
	)
	s.Unlock()
	if err != nil {
		log.Errorf("err=%v", err)
	}
}

func (s *Signal) Close() {
	log.Infof("[Signal.Close]")
	s.cancel()
}
