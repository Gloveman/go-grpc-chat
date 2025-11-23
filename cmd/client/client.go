package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	pb "github.com/Gloveman/go-grpc-chat/chatpb"
	"github.com/sqweek/dialog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type ReceivedFile struct {
	ID     string
	Name   string
	Sender string
	Time   time.Time
}

// client 상태 변수
var (
	grpcClient  pb.ChatServiceClient
	userName    string
	recentFiles []ReceivedFile
)

func main() {
	reader := bufio.NewReader(os.Stdin)

	var conn *grpc.ClientConn
	var err error
	for {
		//접속할 서버 주소 입력
		fmt.Print("Enter Server IP(default:localhost): ")
		serverIP, _ := reader.ReadString('\n')
		serverIP = strings.TrimSpace(serverIP)
		if serverIP == "" {
			serverIP = "localhost"
		}
		serverAddress := fmt.Sprintf("%s:50001", serverIP)
		log.Printf("%s 접속중.....", serverAddress)

		conn, err = grpc.NewClient(serverAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			log.Fatalf("Failed to connect: %v", err)
			continue
		}
		//ChatService의 gRPC 클라이언트 생성
		grpcClient = pb.NewChatServiceClient(conn)

		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		_, err = grpcClient.GetRoomsInfo(ctx, &pb.RoomsInfoRequest{})
		cancel()

		if err != nil {
			conn.Close() // 실패한 연결 닫기
			fmt.Printf("❌ 서버 접속 실패: %v\nIP를 다시 확인해주세요.\n\n", err)
			continue
		}

		log.Println("✅ 서버 연결 성공!")
		break // 연결 성공 시 루프 탈출
	}
	defer conn.Close()

	var connectStream pb.ChatService_ConnectClient
	//사용자 이름 설정
	for {
		fmt.Print("Enter your name: ")
		inputName, _ := reader.ReadString('\n')
		inputName = strings.TrimSpace(inputName)
		if inputName == "" {
			log.Printf("오류: 닉네임을 입력하지 않았습니다.")
			continue
		}
		connectStream, err = grpcClient.Connect(context.Background(), &pb.ConnectRequest{UserName: inputName})
		if err != nil {
			log.Printf("오류: %v", err)
			continue
		}
		_, err = connectStream.Recv()
		if err != nil {
			log.Printf("오류: %v", err)
			continue
		}
		userName = inputName
		log.Printf("%s님, 채팅 서비스에 오신 것을 환영합니다.", userName)
		printRoomsInfo()
		break
	}
	//DM 수신
	go func() {
		for {
			msg, err := connectStream.Recv()
			if err != nil {
				log.Fatal("서버 연결이 끊어졌습니다")
			}
			if msg.SenderUserId != "서버" {
				if msg.FileId != "" {
					f := ReceivedFile{
						ID:     msg.FileId,
						Name:   msg.FileName,
						Sender: msg.SenderUserName,
						Time:   time.Now(),
					}
					recentFiles = append(recentFiles, f)
					fileIndex := len(recentFiles)

					fmt.Printf("\n[DM from %s] 📎 파일 도착!\n", msg.SenderUserName)
					fmt.Printf("📄 %s (ID: %s)\n", msg.FileName, msg.FileId)
					fmt.Printf("> 다운로드: /download %d (또는 /download %s)\n> ", fileIndex, msg.FileId)
				} else {
					fmt.Printf("\n[DM from %s]: %s\n> ", msg.SenderUserName, msg.MessageText)
				}
			}
		}
	}()
	// '로비' 구현
	for {
		time.Sleep(300 * time.Millisecond)

		fmt.Print("\n명령어 입력(help로 명령어 목록 확인): ")
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		if strings.ToLower(input) == "help" {
			printLobbyHelp()
			continue
		}
		if strings.HasPrefix(input, "/w ") {
			sendDM(input)
			continue
		}
		if strings.ToLower(input) == "users" {
			printAllUsers()
			continue
		}
		if strings.ToLower(input) == "list" {
			printRoomsInfo()
			continue
		}
		if strings.ToLower(input) == "files" {
			printFiles()
			continue
		}
		if strings.HasPrefix(strings.ToLower(input), "/download ") {
			arg := strings.TrimSpace(strings.TrimPrefix(input, "/download "))
			if arg == "" {
				fmt.Println("사용법: /download [번호] 또는 [파일ID]")
				continue
			}
			targetID := arg
			if idx, err := strconv.Atoi(arg); err == nil {
				// 번호로 입력된 경우
				if idx >= 1 && idx <= len(recentFiles) {
					targetID = recentFiles[idx-1].ID
					fmt.Printf("목록 #%d (%s) 다운로드를 시작합니다.\n", idx, recentFiles[idx-1].Name)
				} else {
					fmt.Println("잘못된 파일 번호입니다.")
					continue
				}
			}

			downloadFile(targetID)
			continue
		}
		if strings.HasPrefix(input, "/wfile ") {
			parts := strings.Fields(input)
			if len(parts) < 2 {
				fmt.Println("사용법: /wfile [유저 이름]")
				continue
			}
			targetUser := parts[1]
			sendFile(0, targetUser)
			continue
		}
		if strings.HasPrefix(strings.ToLower(input), "create ") {
			roomName := input[7:]
			if roomName == "" {
				log.Println("오류: 방 이름을 입력하지 않았습니다.")
				continue
			}
			startChatSession(0, roomName)
		}
		if strings.HasPrefix(strings.ToLower(input), "join ") {
			roomID_num, err := strconv.Atoi(input[5:])
			if err != nil {
				log.Println("오류: 방 번호는 숫자로 입력해야 합니다.")
				continue
			}
			startChatSession(int32(roomID_num), "")
		}
		if strings.ToLower(input) == "quit" {
			break
		}
	}
	log.Println("채팅 서비스를 종료합니다. 이용해주셔서 감사합니다.")
	time.Sleep(500 * time.Millisecond)
}

func startChatSession(roomId int32, roomName string) {
	joinReq := &pb.JoinRequest{
		UserName: userName,
		RoomId:   roomId,
		RoomName: roomName,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream, err := grpcClient.JoinRoom(ctx, joinReq)
	if err != nil {
		log.Printf("방 입장 또는 생성에 실패했습니다: %v", err)
		return
	}

	firstMsg, err := stream.Recv() //최초 수신을 통해 방 번호를 가져옴
	if err != nil {
		log.Printf("방 입장 실패 (서버 응답 없음): %v", err)
		return
	}

	if roomId == 0 {
		roomId = firstMsg.RoomId
		log.Printf("새로운 방 [%d번: %s]이 생성되었습니다!", roomId, roomName)
	}

	fmt.Printf("[%s]: %s\n", firstMsg.SenderUserName, firstMsg.MessageText)

	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				log.Printf("서버와 연결이 종료되었습니다. (로비로 돌아갑니다)")
				cancel() //input loop 중지
				return
			}
			if msg.FileId != "" {
				// 파일이 첨부된 메시지인 경우
				f := ReceivedFile{
					ID:     msg.FileId,
					Name:   msg.FileName,
					Sender: msg.SenderUserName,
					Time:   time.Now(),
				}
				recentFiles = append(recentFiles, f)
				fileIndex := len(recentFiles)
				fmt.Println("------------------------------------------------------")
				fmt.Printf("[%s]님이 파일을 업로드했습니다.\n", msg.SenderUserName)
				fmt.Printf("📄 파일명: %s\n", msg.FileName)
				fmt.Printf("🔗 파일ID: %s\n", msg.FileId)
				fmt.Printf("⬇️ 다운로드: /down %d (또는 /down %s)\n", fileIndex, msg.FileId)
				fmt.Println("------------------------------------------------------")
			} else {
				// 일반 채팅 메시지
				fmt.Printf("[%s]: %s\n", msg.SenderUserName, msg.MessageText)
			}
		}
	}()

	reader := bufio.NewReader(os.Stdin)
	time.Sleep(300 * time.Millisecond)
	printRoomHelp()
	for {
		//서버 연결이 끊어졌는지 확인
		select {
		case <-ctx.Done():
			return
		default:
		}
		fmt.Print(">")
		text, _ := reader.ReadString('\n')
		text = strings.TrimSpace(text)

		//연결 상태 다시 검사
		if ctx.Err() != nil {
			return
		}
		if strings.ToLower(text) == "/quit" {
			log.Println("현재 방에서 퇴장합니다.")
			return
		}
		if text == "" {
			continue
		}
		if strings.HasPrefix(text, "/w ") {
			sendDM(text)
			continue
		}
		if strings.ToLower(text) == "/users" {
			printAllUsers()
			continue
		}
		if strings.ToLower(text) == "/roomusers" {
			printRoomUsers(roomId)
			continue
		}
		if strings.ToLower(text) == "/files" {
			printFiles()
			continue
		}
		if strings.ToLower(text) == "/upload" {
			sendFile(roomId, "")
			continue
		}
		if strings.HasPrefix(text, "/wfile ") {
			parts := strings.Fields(text)
			if len(parts) < 2 {
				fmt.Println("사용법: /wfile [유저 이름]")
				continue
			}
			targetUser := parts[1]
			sendFile(0, targetUser)
			continue
		}
		if strings.HasPrefix(strings.ToLower(text), "/download ") {
			arg := strings.TrimSpace(strings.TrimPrefix(text, "/download "))
			if arg == "" {
				fmt.Println("사용법: /download [번호] 또는 [파일ID]")
				continue
			}
			targetID := arg
			if idx, err := strconv.Atoi(arg); err == nil {
				// 번호로 입력된 경우
				if idx >= 1 && idx <= len(recentFiles) {
					targetID = recentFiles[idx-1].ID
					fmt.Printf("목록 #%d (%s) 다운로드를 시작합니다.\n", idx, recentFiles[idx-1].Name)
				} else {
					fmt.Println("잘못된 파일 번호입니다.")
					continue
				}
			}

			downloadFile(targetID)
			continue
		}
		if strings.ToLower(text) == "/help" {
			printRoomHelp()
			continue
		}
		//메세지 전송에 Timeout 적용
		sendCtx, sendCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := grpcClient.SendMessage(sendCtx, &pb.ChatMessage{
			SenderUserName: userName,
			MessageText:    text,
			RoomId:         roomId,
		})
		sendCancel()
		if err != nil {
			log.Printf("메시지 전송 실패: %v", err)
		}
	}
}

func sendDM(input string) {
	parts := strings.SplitN(input, " ", 3)
	if len(parts) < 3 {
		fmt.Println("사용법: /w [대상유저] [메시지]")
		return
	}
	targetUser := parts[1]
	message := parts[2]

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := grpcClient.SendMessage(ctx, &pb.ChatMessage{
		SenderUserName: userName,
		TargetUserId:   targetUser,
		MessageText:    message,
	})
	if err != nil {
		fmt.Printf("전송 실패: %v\n", err)
	} else {
		fmt.Printf("[DM to %s]: %s\n", targetUser, message)
	}
}

func sendFile(roomId int32, targetUser string) {
	filePath, err := dialog.File().Load()
	if err != nil {
		if err.Error() == "Cancelled" {
			fmt.Println("파일 선택 취소")
		}
	}
	if filePath == "" {
		fmt.Println("파일 선택이 취소되었습니다.")
		return
	}
	uploadAndSend(filePath, roomId, targetUser)
}

func downloadFile(fileID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	stream, err := grpcClient.DownloadFile(ctx, &pb.DownloadRequest{
		FileId:          fileID,
		RequestUserName: userName,
	})
	if err != nil {
		log.Printf("다운로드 요청 실패: %v", err)
		return
	}

	var file *os.File
	var totalBytes int64

	//파일 메타데이터 먼저 수신
	firstChunk, err := stream.Recv()
	if err != nil {
		log.Printf("메타데이터 수신 실패: %v", err)
		return
	}

	info := firstChunk.GetInfo()
	if info == nil {
		log.Println("오류: 서버로부터 파일 정보를 받지 못했습니다.")
		return
	}

	originalFileName := info.FileName

	savePath, err := dialog.File().
		Title("파일 저장").
		SetStartFile(originalFileName).
		Save()

	if err != nil {
		if err.Error() == "Cancelled" {
			fmt.Println("다운로드가 취소되었습니다.")
		}
	}

	//dialog 닫은 경우
	if savePath == "" {
		return
	}

	file, err = os.Create(savePath)
	if err != nil {
		log.Printf("파일 생성 실패: %v", err)
		return
	}
	defer file.Close()
	fmt.Printf("다운로드 중... (%s)\n", savePath)

	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("다운로드 중 끊김: %v", err)
			// 실패 시 잔존 파일 삭제 고려
			file.Close()
			os.Remove(savePath)
			return
		}

		if data := chunk.GetChunkData(); data != nil {
			n, writeErr := file.Write(data)
			if writeErr != nil {
				log.Printf("파일 쓰기 실패: %v", writeErr)
				return
			}
			totalBytes += int64(n)
		}
	}
	message := fmt.Sprintf("다운로드 완료 (%.2f KB)", float64(totalBytes)/1024.0)
	//MB 단위인 경우
	if totalBytes > 1024*1024 {
		message = fmt.Sprintf("다운로드 완료 (%.2f MB)", float64(totalBytes)/1024.0/1024.0)
	}
	fmt.Println(message)
}
func uploadAndSend(filePath string, roomId int32, targetUser string) {
	file, err := os.Open(filePath)
	if err != nil {
		log.Printf("파일 열기 실패: %v", err)
		return
	}
	defer file.Close()

	stream, err := grpcClient.UploadFile(context.Background())
	if err != nil {
		log.Printf("업로드 스트림 생성 실패: %v", err)
		return
	}

	//파일 메타데이터 생성 및 전송
	fileName := filepath.Base(filePath)
	req := &pb.FileChunk{
		Data: &pb.FileChunk_Info{
			Info: &pb.FileInfo{
				FileName:     fileName,
				RoomId:       roomId,
				TargetUserId: targetUser,
			},
		},
	}
	if err := stream.Send(req); err != nil {
		log.Printf("메타데이터 전송 실패: %v", err)
		return
	}
	//파일 데이터 보내기 (chunk 단위)
	buf := make([]byte, 64*1024)
	for {
		n, err := file.Read(buf)
		if n > 0 {
			if err := stream.Send(&pb.FileChunk{
				Data: &pb.FileChunk_ChunkData{ChunkData: buf[:n]},
			}); err != nil {
				log.Printf("데이터 전송 실패: %v", err)
				return
			}
		}
		if err == io.EOF {
			break
		}
	}

	res, err := stream.CloseAndRecv()
	if err != nil {
		log.Printf("업로드 마무리에 실패했습니다: %v", err)
		return
	}
	fmt.Printf("업로드 완료! (파일 ID: %s)\n", res.FileId)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = grpcClient.SendMessage(ctx, &pb.ChatMessage{
		SenderUserName: userName,
		RoomId:         roomId,
		TargetUserId:   targetUser,
		MessageText:    "",
		FileId:         res.FileId,
		FileName:       fileName,
	})
	if err != nil {
		log.Printf("알림 전송 실패: %v", err)
	}
}

func printLobbyHelp() {
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Println("📍 로비 명령어:")
	fmt.Println("  create [방이름]            - 새 방 만들기")
	fmt.Println("  join [방번호]              - 방 입장")
	fmt.Println("  list                      - 방 목록")
	fmt.Println("  /w [유저명] [메시지]       - DM 보내기")
	fmt.Println("  /wfile [유저명]           - DM으로 파일 보내기")
	fmt.Println("  /download [fileId]        - 파일 다운로드")
	fmt.Println("  files                      - 받은 파일 목록")
	fmt.Println("  users                      - 전체 접속 유저 목록")
	fmt.Println("  help                       - 도움말")
	fmt.Println("  quit                      - 종료")
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
}

func printRoomHelp() {
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Println("💬 채팅방 명령어:")
	fmt.Println("  /w [유저명] [메시지]       - DM 보내기")
	fmt.Println("  /wfile [유저명]       - DM으로 파일 보내기")
	fmt.Println("  /upload                  - 현재 방에 파일 전송")
	fmt.Println("  /download [fileId]             - 파일 다운로드")
	fmt.Println("  /users                      - 전체 유저 목록")
	fmt.Println("  /files                      - 받은 파일 목록")
	fmt.Println("  /roomusers                      - 현재 방 유저 목록")
	fmt.Println("  /help                       - 도움말")
	fmt.Println("  /quit                       - 방 나가기")
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
}
func printRoomsInfo() {
	//방 목록 조회에 Timeout 적용
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	roomsInfo, err := grpcClient.GetRoomsInfo(ctx, &pb.RoomsInfoRequest{})
	if err != nil {
		log.Printf("방 목록을 가져오지 못했습니다. 오류: %v", err)
	}

	fmt.Println("\n ---현재 접속 가능한 방 리스트---")
	if len(roomsInfo.Rooms) == 0 {
		fmt.Println("(생성된 방 없음)")
	}
	fmt.Printf("%-5s | %-20s | %s\n", "번호", "이름", "현재 인원")
	fmt.Println("----------------------------------------")
	for _, room := range roomsInfo.Rooms {
		fmt.Printf("%-5d | %-20s | %-5d\n", room.RoomId, room.RoomName, room.ClientCount)
	}
	fmt.Println("----------------------------------------")
}

func printAllUsers() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := grpcClient.GetAllUsers(ctx, &pb.AllUsersRequest{})
	if err != nil {
		log.Printf("전체 유저 목록을 불러오지 못했습니다: %v", err)
		return
	}
	fmt.Println("--- 전체 접속 유저 ---")
	for _, u := range res.Users {
		fmt.Printf("- %s\n", u.UserName)
	}
	fmt.Println("---------------------")
}

func printRoomUsers(roomID int32) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := grpcClient.GetRoomUsers(ctx, &pb.RoomUsersRequest{RoomId: roomID})
	if err != nil {
		log.Printf("현재 방 유저 목록을 불러오지 못했습니다: %v", err)
		return
	}
	fmt.Println("--- 방 접속 유저 ---")
	for _, u := range res.Users {
		fmt.Printf("- %s\n", u.UserName)
	}
	fmt.Println("---------------------")
}

func printFiles() {
	fmt.Println("\n--- 📂 수신된 전체 파일 목록 (Session) ---")

	if len(recentFiles) == 0 {
		fmt.Println("(수신된 파일이 없습니다)")
	} else {
		for i, f := range recentFiles {

			// [번호] 파일명 (보낸이 | 출처 | 시간)
			fmt.Printf("[%d] %s (From: %s | %s)\n",
				i+1, f.Name, f.Sender, f.Time.Format("00:00"))
		}
		fmt.Println("------------------------------------------")
		fmt.Println("Tip: 다운로드는 '/down [번호]'를 입력하세요.")
	}
}
