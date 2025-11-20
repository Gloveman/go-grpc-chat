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

const (
	defaultBufferSize = 100
	maxBufferSize     = 500
	minBufferSize     = 10
)

type Room struct {
	ID      int32
	Name    string                          // 방 이름
	Clients map[string]chan *pb.ChatMessage // Message channel로 재정의
	mu      sync.RWMutex
}
type server struct {
	pb.UnimplementedChatServiceServer

	rooms map[int32]*Room // 방 ID : Room 구조체 map

	mu sync.RWMutex

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
			ID:      roomID,
			Name:    roomName,
			Clients: make(map[string]chan *pb.ChatMessage),
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
	s.mu.Unlock()
	msgChan := make(chan *pb.ChatMessage, 100) // Buffer of 100

	targetRoom.mu.Lock()
	targetRoom.Clients[userName] = msgChan
	targetRoom.mu.Unlock()

	// 1. 먼저 입장한 유저에게만 환영 메시지 전송 (방 정보 포함)
	welcomeMessage := &pb.ChatMessage{
		SenderUserName: "서버",
		MessageText:    fmt.Sprintf("[%s] 방에 입장했습니다. (방 번호: %d)", roomName, roomID),
		RoomId:         roomID,
	}

	select {
	case msgChan <- welcomeMessage:
	default:
	}

	joinMessage := &pb.ChatMessage{
		SenderUserName: "서버",
		MessageText:    fmt.Sprintf("%s 님이 입장했습니다.", userName),
		RoomId:         roomID,
	}
	s.broadcastMessage(targetRoom, joinMessage)

	defer func() {
		targetRoom.mu.Lock()
		delete(targetRoom.Clients, userName)
		isEmpty := len(targetRoom.Clients) == 0
		targetRoom.mu.Unlock()

		log.Printf("유저 [%s]가 방 [%s](방 번호: %d)에서 퇴장", userName, roomName, roomID)

		leaveMessage := &pb.ChatMessage{
			SenderUserName: "서버",
			MessageText:    fmt.Sprintf("%s 님이 퇴장했습니다.", userName),
			RoomId:         roomID,
		}
		s.broadcastMessage(targetRoom, leaveMessage)
		if isEmpty {
			s.mu.Lock()
			targetRoom.mu.RLock()
			if len(targetRoom.Clients) == 0 { // Double Check!
				delete(s.rooms, roomID)
				log.Printf("방 [%s](방 번호: %d)가 삭제됨", roomName, roomID)
			}
			targetRoom.mu.RUnlock()
			s.mu.Unlock()
		}
	}()

	ctx := srv.Context()
	for {
		select {
		case msg := <-msgChan:
			if err := srv.Send(msg); err != nil {
				log.Printf("유저 [%s]에게 메시지 전송 실패: %v", userName, err)
				return err
			}
		case <-ctx.Done():
			log.Printf("유저 [%s] 연결 종료 감지", userName)
			return nil
		}

	}

}

func (s *server) SendMessage(ctx context.Context, msg *pb.ChatMessage) (*pb.SendResponse, error) {
	roomID := msg.GetRoomId()
	s.mu.RLock()
	room, ok := s.rooms[roomID]
	s.mu.RUnlock()
	if !ok {
		log.Printf("SendMessage 오류: 방 %d를 찾을 수 없음", roomID)
		return &pb.SendResponse{Success: false}, status.Errorf(codes.NotFound, "Room not found")
	}
	log.Printf("메시지 수신 (방 번호 : %d) [%s]: %s", roomID, msg.SenderUserName, msg.MessageText)
	s.broadcastMessage(room, msg)
	return &pb.SendResponse{Success: true}, nil
}

func (s *server) broadcastMessage(room *Room, msg *pb.ChatMessage) { // 해당 방의 모두에게 메시지 전송
	room.mu.RLock()
	defer room.mu.RUnlock()
	for name, ch := range room.Clients {
		select {
		case ch <- msg:
		default:
			log.Printf("유저 [%s]의 수신 버퍼가 가득 찼습니다. 메시지를 드롭합니다.", name)
		}
	}
}

func (s *server) GetRoomsInfo(ctx context.Context, req *pb.RoomsInfoRequest) (*pb.RoomsInfoResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var roomInfos []*pb.RoomInfo
	for roomID, room := range s.rooms {
		room.mu.RLock()
		count := int32(len(room.Clients))
		room.mu.RUnlock()
		roomInfos = append(roomInfos, &pb.RoomInfo{
			RoomId:      roomID,
			RoomName:    room.Name,
			ClientCount: count,
		})
	}
	return &pb.RoomsInfoResponse{Rooms: roomInfos}, nil
}
