package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	pb "github.com/Gloveman/go-grpc-chat/chatpb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	conn, err := grpc.NewClient("localhost:50001", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close() // main 함수 종료 시 연결 종료

	// 4. ChatService의 gRPC 클라이언트 생성
	client := pb.NewChatServiceClient(conn)

	// --- 1단계: 사용자 이름 설정 ---
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Enter your name: ")
	userName, _ := reader.ReadString('\n')
	userName = strings.TrimSpace(userName)

	// --- 2단계: JoinRoom 호출 (스트림 시작) ---
	joinReq := &pb.JoinRequest{
		UserName: userName,
	}

	// 5. 서버에 JoinRoom 요청을 보내고 '스트림(stream)'을 받음
	stream, err := client.JoinRoom(context.Background(), joinReq)
	if err != nil {
		log.Fatalf("Failed to join room: %v", err)
	}
	log.Printf("%s 님, 채팅방에 오신 것을 환영합니다!", userName)

	// --- 3단계: 메시지 수신 (별도 고루틴) ---
	// 6. (핵심!) 서버로부터 메시지를 '수신'하는 전용 고루틴을 실행
	go func() {
		for {
			// 7. stream.Recv()는 서버가 메시지를 보낼 때까지 '대기(block)'
			msg, err := stream.Recv()
			if err != nil {
				// (서버가 끊기거나 종료되면 에러 발생)
				log.Printf("Stream error, exiting: %v", err)
				return
			}

			// 8. 화면에 메시지 출력
			// (내가 보낸 메시지도 서버를 거쳐 여기서 출력됨)
			fmt.Printf("[%s]: %s\n", msg.SenderUserName, msg.MessageText)
		}
	}()

	// --- 4단계: 메시지 송신 (메인 고루틴) ---
	// 9. 메인 고루틴은 '송신'을 담당 (키보드 입력 대기)
	for {
		// 10. 터미널에서 한 줄 입력받기
		fmt.Printf("%s: ", userName)
		text, _ := reader.ReadString('\n')
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}

		if strings.ToLower(text) == "quit" {
			log.Println("채팅을 종료합니다...")
			break // 2. for 루프를 탈출(break)
		}

		// 11. 서버에 SendMessage RPC 호출
		_, err := client.SendMessage(context.Background(), &pb.ChatMessage{
			SenderUserName: userName, // (1단계에서는 서버가 이 이름을 무시함)
			MessageText:    text,
		})
		if err != nil {
			log.Printf("Failed to send message: %v", err)
		}
	}
	log.Println("연결 종료")
}
