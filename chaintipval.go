package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
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

func connectToNode(nodeIP string, netParams *chaincfg.Params) {
	// conn 정의 추가
	conn, err := net.Dial("tcp", nodeIP)
	if err != nil {
		log.Fatalf("Failed to connect to node: %v", err)
	}
	defer conn.Close()
	fmt.Println("Connected to node:", nodeIP)

	// Version 메시지 전송 준비
	localAddr := conn.LocalAddr().(*net.TCPAddr)
	remoteAddr := conn.RemoteAddr().(*net.TCPAddr)
	var serviceFlag wire.ServiceFlag
	serviceFlag = wire.SFNodeNetworkLimited

	verMsg := wire.NewMsgVersion(
		wire.NewNetAddressIPPort(localAddr.IP, uint16(localAddr.Port), serviceFlag),
		wire.NewNetAddressIPPort(remoteAddr.IP, uint16(remoteAddr.Port), 0),
		0,
		0,
	)

	err = wire.WriteMessage(conn, verMsg, 0, netParams.Net)
	if err != nil {
		log.Fatalf("Failed to send version message: %v", err)
	}
	fmt.Println("Sent version message")

	// ... (이전 코드 생략: conn 연결, version 메시지 전송 등)
	for {
		msg, _, err := wire.ReadMessage(conn, 0, netParams.Net)
		if err != nil {
			log.Fatalf("Failed to read message: %v", err)
		}

		switch m := msg.(type) {
		case *wire.MsgVerAck:
			fmt.Println("Received verack")
			// 수정된 부분: 상대에게 verack 응답 전송
			verAckMsg := wire.NewMsgVerAck()                           // verack 메시지 생성
			err = wire.WriteMessage(conn, verAckMsg, 0, netParams.Net) // verack 전송
			if err != nil {
				log.Fatalf("Failed to send verack message: %v", err) // 전송 실패 시 에러 처리
			}
			fmt.Println("Sent verack response") // 전송 확인 출력
			// 핸드셰이크 완료 후 블록 요청
			requestBlocks(conn, netParams)
			return
		default:
			fmt.Printf("Received other message: %T\n", m)
		}
	}
}

// 4. 블록 요청 함수
func requestBlocks(conn net.Conn, netParams *chaincfg.Params) {
	genesisHash := netParams.GenesisHash
	getBlocksMsg := &wire.MsgGetBlocks{
		ProtocolVersion:    wire.ProtocolVersion,
		BlockLocatorHashes: []*chainhash.Hash{genesisHash},
		HashStop:           chainhash.Hash{},
	}

	err := wire.WriteMessage(conn, getBlocksMsg, 0, netParams.Net)
	if err != nil {
		log.Fatalf("Failed to send getblocks message: %v", err)
	}
	fmt.Println("Sent getblocks request")

	requested := false // getdata 요청 상태 추적

	for {
		msg, _, err := wire.ReadMessage(conn, 0, netParams.Net)
		if err != nil {
			if me, ok := err.(*wire.MessageError); ok && me.Description == "payload exceeds max length" {
				fmt.Println("Received invalid message (size limit exceeded), skipping:", err)
				return //continue에서 리턴으로 바꿈
			}
			log.Printf("Failed to read message: %v", err)
			continue
		}

		switch m := msg.(type) {
		case *wire.MsgInv:
			if requested {
				fmt.Println("Ignoring additional MsgInv while waiting for MsgBlock") //추가 블록이 없이면 여기서 막힘힘
				return                                                               // getdata 후 추가 MsgInv 무시 / continue에서 리턴으로 바꿈
			}
			fmt.Printf("Received inventory message: %d blocks available\n", len(m.InvList))
			getDataMsg := wire.NewMsgGetData()
			for _, inv := range m.InvList {
				if inv.Type == wire.InvTypeBlock {
					getDataMsg.AddInvVect(inv)
				}
			}
			err = wire.WriteMessage(conn, getDataMsg, 0, netParams.Net)
			if err != nil {
				log.Printf("Failed to send getdata message: %v", err)
				return //continue에서 리턴으로 바꿈
			}
			fmt.Println("Sent getdata request for all blocks")
			requested = true // 요청 보냄 표시
		case *wire.MsgBlock:
			fmt.Printf("Received block: %s, TxCount: %d\n",
				m.BlockHash().String(), len(m.Transactions))
			processBlock(m)
			requested = false // 블록 받았으니 다음 MsgInv 허용
		case *wire.MsgReject:
			fmt.Printf("Received reject message: Command=%s, Code=%d, Reason=%s\n",
				m.Cmd, m.Code, m.Reason)
			return //continue에서 리턴으로 바꿈
		default:
			fmt.Printf("Received other message: %T\n", m)
		}
	}
}

// requestBlocks 밖으로 `processBlock()`을 이동시켜야해서 이동 시킴
func processBlock(block *wire.MsgBlock) {
	fmt.Println("Processing block:", block.BlockHash().String())
	// 블록 높이, 트랜잭션 개수 출력
	fmt.Printf("Transaction Count: %d\n", len(block.Transactions))
	// 첫 번째 트랜잭션 정보 출력
	if len(block.Transactions) > 0 {
		fmt.Printf("Recent transaction ID: %s\n", block.Transactions[0].TxHash().String())
	}
	// 트랜잭션 개수 체크
	if len(block.Transactions) == 0 {
		fmt.Println("검증 실패: 트랜잭션이 없는 블록입니다!")
		return
	}
	// 검증 추가: 코인베이스 트랜잭션 확인
	if len(block.Transactions) > 0 {
		firstTx := block.Transactions[0]
		if len(firstTx.TxIn) == 0 {
			fmt.Println("검증 실패: 첫 번째 트랜잭션이 코인베이스가 아닙니다!")
			return
		}
	}
	// 머클 루트 검증
	calculatedMerkleRoot := blockchain.CalcMerkleRoot(block.Transactions)
	fmt.Printf("Calculated Merkle Root: %s\n", calculatedMerkleRoot.String())
	fmt.Printf("Header Merkle Root: %s\n", block.Header.MerkleRoot.String())
	if calculatedMerkleRoot != block.Header.MerkleRoot {
		fmt.Println("검증 실패: 머클 루트가 일치하지 않습니다!")
		return
	}
	// 추가 정보: 난이도 Bits 출력 (선택적)
	fmt.Printf("Block Difficulty Bits: %x\n", block.Header.Bits)
	fmt.Printf("Merkle Root: %s\n", block.Header.MerkleRoot.String()) //머클루트꺼
	fmt.Println("블록 검증 완료!")
}
