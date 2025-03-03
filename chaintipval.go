package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/wire"
	"github.com/utreexo/utreexod/chaincfg/chainhash"
)

func main() {
	signet := flag.Bool("signet", false, "Enable Signet network")
	testnet3 := flag.Bool("testnet3", false, "Enable Testnet3 network")
	connect := flag.String("connect", "", "IP")

	flag.Parse()

	if *signet && *testnet3 {
		fmt.Println("Error: --signet and --testnet3 cannot be used together.")
		os.Exit(1)
	}

	if *connect == "" {
		fmt.Println("Error: --connect flag is required.")
		fmt.Println("Usage: --connect <IP address>")
		os.Exit(1)
	}

	var netParams *chaincfg.Params
	var defaultPort string
	switch {
	case *signet:
		netParams = &chaincfg.SigNetParams
		defaultPort = "38333"
		fmt.Println("Using Signet network")
	case *testnet3:
		netParams = &chaincfg.TestNet3Params
		defaultPort = "18333"
		fmt.Println("Using Testnet3 network")
	default:
		fmt.Println("Error: Please specify --signet or --testnet3")
		os.Exit(1)
	}
	host, port, err := net.SplitHostPort(*connect)
	if err != nil {
		host = *connect
		port = defaultPort // defaultPort 사용
	}

	// IP 검증
	ipAddr := net.ParseIP(host)
	if ipAddr == nil {
		fmt.Println("Wrong IP Address.Try again")
		os.Exit(1)
	}

	// 최종적으로 "IP:PORT" 조합
	fullAddress := fmt.Sprintf("%s:%s", host, port)
	fmt.Printf("Connecting to node: %s\n", fullAddress)

	connectToNode(fullAddress, netParams)
}

func connectToNode(nodeIP string, netParams *chaincfg.Params) { // 여기서 넷params 정의
	conn, err := net.Dial("tcp", nodeIP)
	if err != nil {
		log.Fatalf("Failed to connect to node: %v", err)
	}
	defer conn.Close() // 이거 defer하면 연결 된 다음 연결 종료되는 함수같은데 나는 계속 정보를 주고 받아야하니 종료 안되게 얘가 사라져야하지 않을까...?
	fmt.Println("Connected to node:", nodeIP)

	// 2. 버전 핸드쉨 (version 메시지 전송) 하는 이유는 올바른 넷웤끼리 통신 주고 받을려면 해야한다는데 왜 내 ip주소를 보내야하지? 이리로 오게 할려고 그런건가
	localAddr := conn.LocalAddr().(*net.TCPAddr)   // 내 주소
	remoteAddr := conn.RemoteAddr().(*net.TCPAddr) // 노드 주소

	// 서비스 플래그를 0으로 설정 (이것도 비트코인 라이브러리인데... 다른 서비스 플래그도 많은데 얘 하나만 있어도 되나? 많은 정보 줄 수 있으면 더 좋은거 아닌가)
	serviceFlag := wire.SFNodeNetwork | wire.SFNodeGetUTXO

	// 버전 메시지 생성
	verMsg := wire.NewMsgVersion(
		wire.NewNetAddressIPPort(localAddr.IP, uint16(localAddr.Port), serviceFlag),   // 내 주소
		wire.NewNetAddressIPPort(remoteAddr.IP, uint16(remoteAddr.Port), serviceFlag), // 상대 주소
		0,
		0)

	// 메시지 전송
	err = wire.WriteMessage(conn, verMsg, 0, netParams.Net)
	if err != nil {
		log.Fatalf("Failed to send version message: %v", err)
	}
	fmt.Println("Sent version message")

	// 3. 응답 읽기
	for {
		msg, _, err := wire.ReadMessage(conn, 0, netParams.Net)
		if err != nil {
			log.Fatalf("Failed to read message: %v", err)
		}

		switch m := msg.(type) {
		case *wire.MsgVerAck:
			fmt.Println("Received verack")
			// 버전 핸드셰이크가 완료되었으므로 블록 요청 가능
			requestBlocks(conn, netParams)
			return
		default:
			fmt.Printf("Received other message: %T\n", m)
		}
	}
}

// 4. 블록 요청 함수
func requestBlocks(conn net.Conn, netParams *chaincfg.Params) {
	// stop hash 명시적으로 설정
	getBlocksMsg := &wire.MsgGetBlocks{
		ProtocolVersion:    wire.ProtocolVersion,
		BlockLocatorHashes: []*chainhash.Hash{netParams.GenesisHash}, // 시작점
		HashStop:           chainhash.Hash{},                         // 끝점 (최신까지)
	}

	err := wire.WriteMessage(conn, getBlocksMsg, 0, netParams.Net) // 120번째 줄, netParams 사용
	if err != nil {
		log.Fatalf("Failed to send getblocks message: %v", err)
	}
	fmt.Println("Sent getblocks request")

	// 블록 수신
	for {
		msg, _, err := wire.ReadMessage(conn, 0, netParams.Net)
		if err != nil {
			log.Printf("Failed to read message: %v", err)
			return
		}

		switch m := msg.(type) {
		case *wire.MsgBlock:
			fmt.Println("Received block:", m.BlockHash().String())
		default:
			fmt.Printf("Received other message: %T\n", m)
		}
	}
}
