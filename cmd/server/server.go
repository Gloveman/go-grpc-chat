package main

import (
	"context"
	"log"
	"sync"

	pb "github.com/Gloveman/go-grpc-chat/chatpb"
)

type server struct {
	pb.UnimplementedChatServiceServer

	clients map[string]pb.ChatService_JoinRoomServer

	mu sync.Mutex
}

func main() {

}

func (s *server) JoinRoom(req *pb.JoinRequest, srv pb.ChatService_JoinRoomServer) error {
	userName := req.GetUserName()
	log.Printf("%s 입장", userName)

	s.mu.Lock()
	s.clients[userName] = srv //새 클라이언트 추가
	s.mu.Unlock()

	s.SendMessage(context.Background(), &pb.ChatMessage{
		SenderUserName: "서버",
		MessageText:    userName + "님이 채팅방에 참여했습니다",
	})

	<-srv.Context().Done() //접속 종료 시까지 대기

	log.Printf("%s 퇴장", userName)

	s.mu.Lock()
	delete(s.clients, userName) //클라이언트 제거
	s.mu.Unlock()

	s.SendMessage(context.Background(), &pb.ChatMessage{
		SenderUserName: "서버",
		MessageText:    userName + "님이 채팅방을 나갔습니다",
	})

	return nil
}

func (s *server) SendMessage(ctx context.Context, msg *pb.ChatMessage) (*pb.SendResponse, error) {
	log.Printf("메시지 수신 [%s]: :%s", msg.SenderUserName, msg.MessageText)

	s.mu.Lock()
	for userName, stream := range s.clients {
		if err := stream.Send(msg); err != nil {
			log.Printf("%s에게 메시지 전송 오류: %v", userName, err)
		}
	}
	s.mu.Unlock()

	return &pb.SendResponse{Success: true}, nil
}
