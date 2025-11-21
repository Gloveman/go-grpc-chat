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

	//접속할 서버 주소 입력
	fmt.Print("Enter Server IP: ")
	serverIP, _ := reader.ReadString('\n')
	serverIP = strings.TrimSpace(serverIP)

	serverAddress := fmt.Sprintf("%s:50001", serverIP)
	log.Printf("%s 접속중.....", serverAddress)

	conn, err := grpc.NewClient(serverAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close() // main 함수 종료 시 연결 종료

	//ChatService의 gRPC 클라이언트 생성
	grpcClient = pb.NewChatServiceClient(conn)

	//사용자 이름 설정
	fmt.Print("Enter your name: ")
	inputName, _ := reader.ReadString('\n')
	userName = strings.TrimSpace(inputName)
	log.Printf("%s님, 채팅 서비스에 오신 것을 환영합니다.", userName)

	// '로비' 구현
	for {
		printRoomsInfo()

		fmt.Print("입장할 방 번호를 입력하세요(create [방 이름]으로 새로 만들기 가능, 종료 시 'quit' 입력): ")
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		if strings.HasPrefix(strings.ToLower(input), "create ") {
			roomName := input[7:]
			if roomName == "" {
				log.Println("오류: 방 이름을 입력하지 않았습니다.")
				continue
			}
			startChatSession(0, roomName)
		} else if strings.ToLower(input) == "quit" {
			break
		} else if input != "" {
			roomID_num, err := strconv.Atoi(input)
			if err != nil {
				log.Println("오류: 방 번호는 숫자로 입력해야 합니다.")
				continue
			}
			startChatSession(int32(roomID_num), "")
		}

	}
	log.Println("채팅 서비스를 종료합니다. 이용해주셔서 감사합니다.")
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
		log.Printf("방 입장 또는 생성에 실패했습니다.: %v", err)
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
	for {
		//서버 연결이 끊어졌는지 확인
		select {
		case <-ctx.Done():
			return
		default:
		}
		text, _ := reader.ReadString('\n')
		text = strings.TrimSpace(text)

		//연결 상태 다시 검사
		if ctx.Err() != nil {
			return
		}
		if strings.ToLower(text) == "quit" {
			log.Println("현재 방에서 퇴장합니다.")
			return
		}

		if text == "" {
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
