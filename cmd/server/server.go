package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"

	pb "github.com/Gloveman/go-grpc-chat/chatpb"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Room struct {
	ID      int32
	Name    string                          // 방 이름
	Clients map[string]chan *pb.ChatMessage // Message channel로 재정의
	mu      sync.RWMutex
}

type FileMetadata struct {
	ID           string
	FileName     string
	Path         string
	Owner        string // 보낸 사람
	RoomID       int32  // 보낸 방
	TargetUserID string // 받는 사람(DM)
}
type server struct {
	pb.UnimplementedChatServiceServer

	globalUsers map[string]chan *pb.ChatMessage
	userMu      sync.RWMutex

	rooms   map[int32]*Room // 방 ID : Room 구조체 map
	roomsMu sync.RWMutex

	nextRoomID int32 // 새로운 방 ID 발급을 위한 counter

	files   map[string]*FileMetadata
	filesMu sync.RWMutex
}

func main() {
	lis, err := net.Listen("tcp", ":50001") //50001 포트에서 listen
	if err != nil {
		log.Fatalf("listen 실패: %v", err)
	}

	grpcServer := grpc.NewServer() //grpc 서버 생성

	//server 구조체 생성
	s := &server{
		globalUsers: make(map[string]chan *pb.ChatMessage),
		rooms:       make(map[int32]*Room),
		files:       make(map[string]*FileMetadata),
		nextRoomID:  1, //방 번호는 1번부터 시작
	}

	pb.RegisterChatServiceServer(grpcServer, s)

	log.Println("50001 포트에서 gRPC 서버 시작")
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Serve 실패: %v", err)
	}
}

func (s *server) Connect(req *pb.ConnectRequest, srv pb.ChatService_ConnectServer) error {
	userName := req.GetUserName()

	s.userMu.Lock()
	if _, exists := s.globalUsers[userName]; exists {
		s.userMu.Unlock()
		return status.Errorf(codes.AlreadyExists, "이미 접속 중인 닉네임입니다: %s", userName)
	}

	dmChan := make(chan *pb.ChatMessage, 100)
	s.globalUsers[userName] = dmChan
	s.userMu.Unlock()

	log.Printf("유저 [%s] 서버 접속", userName)

	testMsg := &pb.ChatMessage{
		SenderUserName: "서버",
		MessageText:    "Connected",
	}
	if err := srv.Send(testMsg); err != nil {
		s.userMu.Lock()
		delete(s.globalUsers, userName)
		s.userMu.Unlock()
		return err
	}

	defer func() {
		s.userMu.Lock()
		delete(s.globalUsers, userName)
		s.userMu.Unlock()
		log.Printf("유저 [%s] 서버 접속 종료", userName)
	}()

	ctx := srv.Context()
	for {
		select {
		case msg := <-dmChan:
			if err := srv.Send(msg); err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func (s *server) JoinRoom(req *pb.JoinRequest, srv pb.ChatService_JoinRoomServer) error {
	userName := req.GetUserName()
	roomID := req.GetRoomId()
	roomName := req.GetRoomName()

	s.roomsMu.Lock()

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
			s.roomsMu.Unlock()
			return status.Errorf(codes.NotFound, "방 ID %d를 찾을 수 없습니다.", roomID)
		}
		roomName = targetRoom.Name
		log.Printf("유저 [%s]가 기존 방 [%s](방 번호: %d)에 입장", userName, roomName, roomID)
	}
	s.roomsMu.Unlock()
	roomMsgChan := make(chan *pb.ChatMessage, 100) // Buffer of 100

	targetRoom.mu.Lock()
	targetRoom.Clients[userName] = roomMsgChan
	targetRoom.mu.Unlock()

	// 1. 먼저 입장한 유저에게만 환영 메시지 전송 (방 정보 포함)
	welcomeMessage := &pb.ChatMessage{
		SenderUserName: "서버",
		MessageText:    fmt.Sprintf("[%s] 방에 입장했습니다. (방 번호: %d)", roomName, roomID),
		RoomId:         roomID,
	}

	select {
	case roomMsgChan <- welcomeMessage:
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
			s.roomsMu.Lock()
			targetRoom.mu.RLock()
			if len(targetRoom.Clients) == 0 { // Double Check!
				delete(s.rooms, roomID)
				log.Printf("방 [%s](방 번호: %d)가 삭제됨", roomName, roomID)
			}
			targetRoom.mu.RUnlock()
			s.roomsMu.Unlock()
		}
	}()

	ctx := srv.Context()
	for {
		select {
		case msg := <-roomMsgChan:
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
	//DM인 경우
	if msg.TargetUserId != "" {
		targetUser := msg.TargetUserId
		s.userMu.RLock()
		targetChan, ok := s.globalUsers[targetUser]
		s.userMu.RUnlock()

		if !ok {
			return &pb.SendResponse{Success: false}, nil
		}

		//msg.MessageText = fmt.Sprintf()
		select {
		case targetChan <- msg:
			return &pb.SendResponse{Success: true}, nil
		default:
			return &pb.SendResponse{Success: false}, status.Error(codes.ResourceExhausted, "Buffer full")
		}
	}
	//일반 메세지인 경우
	roomID := msg.GetRoomId()
	s.roomsMu.RLock()
	room, ok := s.rooms[roomID]
	s.roomsMu.RUnlock()
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
	s.roomsMu.RLock()
	defer s.roomsMu.RUnlock()

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

func (s *server) GetAllUsers(ctx context.Context, req *pb.AllUsersRequest) (*pb.AllUsersResponse, error) {
	s.userMu.RLock()
	defer s.userMu.RUnlock()

	var users []*pb.UserInfo
	for name := range s.globalUsers {
		users = append(users, &pb.UserInfo{UserName: name})
	}
	//이름 순으로 정렬
	sort.Slice(users, func(i, j int) bool {
		return users[i].UserName < users[j].UserName
	})
	return &pb.AllUsersResponse{Users: users}, nil
}

func (s *server) GetRoomUsers(ctx context.Context, req *pb.RoomUsersRequest) (*pb.RoomUsersResponse, error) {
	roomID := req.GetRoomId()

	s.roomsMu.RLock()
	room, ok := s.rooms[roomID]
	s.roomsMu.RUnlock()

	if !ok {
		return nil, status.Errorf(codes.NotFound, "방 ID %d를 찾을 수 없습니다.", roomID)
	}

	room.mu.RLock()
	defer room.mu.RUnlock()

	var users []*pb.UserInfo
	for name := range room.Clients {
		users = append(users, &pb.UserInfo{UserName: name})
	}
	sort.Slice(users, func(i, j int) bool {
		return users[i].UserName < users[j].UserName
	})
	return &pb.RoomUsersResponse{Users: users}, nil

}

func (s *server) UploadFile(stream pb.ChatService_UploadFileServer) error {
	var fileID string
	var fileName string
	var filePath string
	var fileMeta *FileMetadata

	uploadDir := "./uploads"
	if _, err := os.Stat(uploadDir); os.IsNotExist(err) {
		os.Mkdir(uploadDir, 0755)
	}

	var file *os.File
	var totalBytes int64
	const MaxFileSize = 1024 * 1024 * 1024 //1GB 제한

	defer func() {
		if file != nil {
			file.Close()
		}
		if file != nil && totalBytes < 0 {
			os.Remove(filePath)
		}
	}()

	for {
		req, err := stream.Recv()
		if err == io.EOF {
			if file != nil {
				file.Close()
				file = nil
			}
			if fileID == "" {
				return status.Errorf(codes.InvalidArgument, "파일 데이터가 없습니다.")
			}

			s.filesMu.Lock()
			s.files[fileID] = fileMeta
			s.filesMu.Unlock()

			message := fmt.Sprintf("파일 저장 완료 (%.2f KB)", float64(totalBytes)/1024.0)
			//MB 단위인 경우
			if totalBytes > 1024*1024 {
				message = fmt.Sprintf("파일 저장 완료 (%.2f MB)", float64(totalBytes)/1024.0/1024.0)
			}
			return stream.SendAndClose(&pb.UploadStatus{
				FileId:  fileID,
				Success: true,
				Message: message,
			})
		}
		if err != nil {
			totalBytes = -1 // Error flag
			return status.Errorf(codes.Unknown, "파일 수신 중 오류: %v", err)
		}
		if info := req.GetInfo(); info != nil {
			fileName = filepath.Base(info.FileName) // 파일 명만 추출
			fileID = uuid.New().String()
			filePath = filepath.Join(uploadDir, fmt.Sprintf("%s_%s", fileID, fileName))
			file, err = os.Create(filePath)
			if err != nil {
				totalBytes = -1
				return status.Errorf(codes.Internal, "파일 생성 실패: %v", err)
			}

			fileMeta = &FileMetadata{
				ID:           fileID,
				FileName:     fileName,
				Path:         filePath,
				RoomID:       info.RoomId,
				TargetUserID: info.TargetUserId,
			}
		} else if chunk := req.GetChunkData(); chunk != nil {
			if file == nil {
				totalBytes = -1
				return status.Errorf(codes.FailedPrecondition, "파일 메타데이터가 먼저 전송되어야 합니다.")
			}
			if totalBytes+int64(len(chunk)) > MaxFileSize {
				totalBytes = -1
				return status.Errorf(codes.ResourceExhausted, "파일 크기가 제한(%dGB)을 초과했습니다.", MaxFileSize/1024/1024/1024)
			}
			n, err := file.Write(chunk)
			if err != nil {
				totalBytes = -1
				return status.Errorf(codes.Internal, "파일 쓰기 실패: %v", err)
			}
			totalBytes += int64(n)
		}
	}
}

func (s *server) DownloadFile(req *pb.DownloadRequest, stream pb.ChatService_DownloadFileServer) error {
	fileID := req.GetFileId()
	requestUser := req.GetRequestUserName()

	s.filesMu.RLock()
	meta, ok := s.files[fileID]
	s.filesMu.RUnlock()

	if !ok {
		return status.Errorf(codes.NotFound, "파일을 찾을 수 없습니다.")
	}

	hasAccess := false

	//파일 접근 권한 확인
	if meta.RoomID > 0 { // 채팅방에서 보낸 경우
		s.roomsMu.RLock()
		room, roomOk := s.rooms[meta.RoomID]
		s.roomsMu.RUnlock()

		if roomOk {
			room.mu.RLock()
			_, inRoom := room.Clients[requestUser]
			room.mu.RUnlock()
			if inRoom {
				hasAccess = true
			}
		}
	} else if meta.TargetUserID != "" { // DM으로 보낸 경우
		if meta.TargetUserID == requestUser {
			hasAccess = true
		}
		if meta.Owner == requestUser { // 보낸 사람도 다운로드 가능하도록 함
			hasAccess = true
		}
	}
	if !hasAccess {
		return status.Errorf(codes.PermissionDenied, "파일 다운로드 권한이 없습니다.")
	}

	file, err := os.Open(meta.Path)
	if err != nil {
		return status.Errorf(codes.Internal, "파일 열기 실패")
	}
	defer file.Close()

	if err := stream.Send(&pb.FileChunk{
		Data: &pb.FileChunk_Info{
			Info: &pb.FileInfo{FileName: meta.FileName},
		},
	}); err != nil {
		return err
	}

	buf := make([]byte, 64*1024)
	for {
		n, err := file.Read(buf)
		if n > 0 {
			if err := stream.Send(&pb.FileChunk{
				Data: &pb.FileChunk_ChunkData{ChunkData: buf[:n]},
			}); err != nil {
				return err
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return status.Errorf(codes.Internal, "읽기 오류")
		}
	}
	return nil

}
