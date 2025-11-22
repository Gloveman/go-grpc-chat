package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	pb "github.com/Gloveman/go-grpc-chat/chatpb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// client 상태 변수
var (
	grpcClient pb.ChatServiceClient
	userName   string
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
				fmt.Printf("\n[DM from %s]: %s\n> ", msg.SenderUserName, msg.MessageText)
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
		if strings.HasPrefix(input, "w ") {
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
			fmt.Printf("[%s]: %s\n", msg.SenderUserName, msg.MessageText)
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
		if text == "/users" {
			printAllUsers()
			continue
		}
		if text == "/roomusers" {
			printRoomUsers(roomId)
			continue
		}
		if text == "/help" {
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

func printLobbyHelp() {
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Println("📍 로비 명령어:")
	fmt.Println("  create [방이름]            - 새 방 만들기")
	fmt.Println("  join [방번호]              - 방 입장")
	fmt.Println("  list                     - 방 목록")
	fmt.Println("  w [유저명] [메시지]       - DM 보내기")
	fmt.Println("  users                     - 전체 접속 유저 목록")
	fmt.Println("  help                       - 도움말")
	fmt.Println("  quit                  - 종료")
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
}

func printRoomHelp() {
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Println("💬 채팅방 명령어:")
	fmt.Println("  /w [유저명] [메시지]       - 방 내 귓속말")
	fmt.Println("  /users                      - 전체 유저 목록")
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
