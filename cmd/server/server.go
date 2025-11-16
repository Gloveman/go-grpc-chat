package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"

	pb "github.com/Gloveman/go-grpc-chat/chatpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Room struct {
	Name    string                                   // 방 이름
	Clients map[string]pb.ChatService_JoinRoomServer // 기존 Clients map과 동일
}
type server struct {
	pb.UnimplementedChatServiceServer

	rooms map[int32]*Room // 방 ID : Room 구조체 map

	mu sync.Mutex

	nextRoomID int32 // 새로운 방 ID 발급을 위한 counter
}

func main() {
	lis, err := net.Listen("tcp", ":50001") //50001 포트에서 listen
	if err != nil {
		log.Fatalf("listen 실패: %v", err)
	}

	grpcServer := grpc.NewServer() //grpc 서버 생성

	//server 구조체 생성
	s := &server{
		rooms:      make(map[int32]*Room),
		nextRoomID: 1, //방 번호는 1번부터 시작
	}

	pb.RegisterChatServiceServer(grpcServer, s)

	log.Println("50001 포트에서 gRPC 서버 시작")
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Serve 실패: %v", err)
	}
}

func (s *server) JoinRoom(req *pb.JoinRequest, srv pb.ChatService_JoinRoomServer) error {
	userName := req.GetUserName()
	roomID := req.GetRoomId()
	roomName := req.GetRoomName()

	s.mu.Lock()

	var targetRoom *Room
	var ok bool

	if roomID == 0 { //새로운 방 생성
		roomID = s.nextRoomID
		s.nextRoomID++

		targetRoom = &Room{
			Name:    roomName,
			Clients: make(map[string]pb.ChatService_JoinRoomServer),
		}
		s.rooms[roomID] = targetRoom
		log.Printf("유저 [%s]가 새로운 방 [%s](방 번호: %d) 생성", userName, roomName, roomID)
	} else { //기존 방 입장
		targetRoom, ok = s.rooms[roomID]
		if !ok {
			s.mu.Unlock()
			return status.Errorf(codes.NotFound, "방 ID %d를 찾을 수 없습니다.", roomID)
		}
		roomName = targetRoom.Name
		log.Printf("유저 [%s]가 기존 방 [%s](방 번호: %d)에 입장", userName, roomName, roomID)
	}

	targetRoom.Clients[userName] = srv
	curRoomID := roomID
	curRoomName := targetRoom.Name
	s.mu.Unlock()
	// 1. 먼저 입장한 유저에게만 환영 메시지 전송 (방 정보 포함)
	welcomeMessage := &pb.ChatMessage{
		SenderUserName: "서버",
		MessageText:    fmt.Sprintf("[%s] 방에 입장했습니다. (방 번호: %d)", curRoomName, curRoomID),
		RoomId:         curRoomID,
	}

	if err := srv.Send(welcomeMessage); err != nil {
		log.Printf("환영 메시지 전송 실패: %v", err)
		// 전송 실패 시 클라이언트 제거
		s.mu.Lock()
		delete(targetRoom.Clients, userName)
		s.mu.Unlock()
		return err
	}

	joinMessage := &pb.ChatMessage{
		SenderUserName: "서버",
		MessageText:    fmt.Sprintf("%s 님이 입장했습니다.", userName),
		RoomId:         curRoomID,
	}
	s.broadcastMessage(curRoomID, joinMessage)

	<-srv.Context().Done() //접속 종료 시까지 대기

	log.Printf("유저 [%s] 연결 종료 감지", userName)

	s.mu.Lock()
	room, ok := s.rooms[curRoomID]
	if !ok {
		s.mu.Unlock()
		return nil
	}

	delete(room.Clients, userName)
	log.Printf("유저 [%s]가 방 [%s](방 번호: %d)에서 퇴장", userName, room.Name, curRoomID)

	leaveMessage := &pb.ChatMessage{
		SenderUserName: "서버",
		MessageText:    fmt.Sprintf("%s 님이 퇴장했습니다.", userName),
		RoomId:         curRoomID,
	}

	for clientName, stream := range room.Clients {
		if err := stream.Send(leaveMessage); err != nil {
			log.Printf("%s에게 퇴장 메시지 전송 오류: %v", clientName, err)
		}
	}

	if len(room.Clients) == 0 {
		delete(s.rooms, curRoomID)
		log.Printf("방 [%s](방 번호: %d)가 삭제됨", room.Name, curRoomID)
	}

	s.mu.Unlock()

	return nil
}

func (s *server) SendMessage(ctx context.Context, msg *pb.ChatMessage) (*pb.SendResponse, error) {
	roomID := msg.GetRoomId()
	log.Printf("메시지 수신 (방 번호 : %d) [%s]: %s", roomID, msg.SenderUserName, msg.MessageText)
	s.broadcastMessage(roomID, msg)
	return &pb.SendResponse{Success: true}, nil
}

func (s *server) broadcastMessage(roomID int32, msg *pb.ChatMessage) { // 해당 방의 모두에게 메시지 전송
	s.mu.Lock()
	defer s.mu.Unlock()

	room, ok := s.rooms[roomID]
	if !ok {
		log.Printf("Broadcast 오류: 방 %d를 찾을 수 없음", roomID)
		return
	}

	var failedClients []string

	for clientName, stream := range room.Clients {
		if err := stream.Send(msg); err != nil {
			log.Printf("%s에게 전송 오류 : %v", clientName, err)
			failedClients = append(failedClients, clientName)
		}
	}

	for _, clientName := range failedClients {
		delete(room.Clients, clientName)
		log.Printf("연결 끊김으로 인해 유저 [%s] 제거", clientName)
	}
}

func (s *server) GetRoomsInfo(ctx context.Context, req *pb.RoomsInfoRequest) (*pb.RoomsInfoResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var roomInfos []*pb.RoomInfo
	for roomID, room := range s.rooms {
		roomInfos = append(roomInfos, &pb.RoomInfo{
			RoomId:      roomID,
			RoomName:    room.Name,
			ClientCount: int32(len(room.Clients)),
		})
	}
	return &pb.RoomsInfoResponse{Rooms: roomInfos}, nil
}
