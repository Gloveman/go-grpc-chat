package main

import (
	"context"
	"log"
	"net"
	"sync"

	pb "github.com/Gloveman/go-grpc-chat/chatpb"
	"google.golang.org/grpc"
)

type server struct {
	pb.UnimplementedChatServiceServer

	clients map[string]pb.ChatService_JoinRoomServer

	mu sync.Mutex
}

func main() {
	lis, err := net.Listen("tcp", ":50001") //50001 포트에서 listen
	if err != nil {
		log.Fatalf("listen 실패: %v", err)
	}

	grpcServer := grpc.NewServer() //grpc 서버 생성

	//server 구조체 생성
	s := &server{
		clients: make(map[string]pb.ChatService_JoinRoomServer),
	}

	pb.RegisterChatServiceServer(grpcServer, s)

	log.Println("50001 포트에서 gRPC 서버 시작")
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Serve 실패: %v", err)
	}
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
	log.Printf("메시지 수신 [%s]: %s", msg.SenderUserName, msg.MessageText)

	s.mu.Lock()
	for userName, stream := range s.clients {
		if err := stream.Send(msg); err != nil {
			log.Printf("%s에게 메시지 전송 오류: %v", userName, err)
		}
	}
	s.mu.Unlock()

	return &pb.SendResponse{Success: true}, nil
}
