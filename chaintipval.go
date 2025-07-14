package main

// 노드와 연결-블록 받기-검증-시스템 종료
import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"

	"github.com/utreexo/utreexod/blockchain"
	"github.com/utreexo/utreexod/btcutil"
	"github.com/utreexo/utreexod/chaincfg"
	"github.com/utreexo/utreexod/chaincfg/chainhash"
	"github.com/utreexo/utreexod/database"
	"github.com/utreexo/utreexod/wire"
)

//

func main() {
	signet := flag.Bool("signet", false, "Enable Signet network")
	testnet3 := flag.Bool("testnet3", false, "Enable Testnet3 network")
	connect := flag.String("connect", "", "IP")
	dataDirFlag := flag.String("datadir", "", "Directory to store data")
	flag.Parse()

	// 데이터 디렉토리 기본값 설정
	var dataDir string
	if *dataDirFlag == "" {
		if runtime.GOOS == "windows" {
			//dataDir = "E:\\Bit\\활동\\코딩\\git\\utreexod\\cmd\\chaintipval"
		} else {
			dataDir = filepath.Join(".", ".utreexod")
		}
	} else {
		dataDir = *dataDirFlag
	}

	// 네트워크 선택 확인
	if *signet && *testnet3 {
		log.Fatal("Error: --signet and --testnet3 cannot be used together.")
	}
	if *connect == "" {
		log.Fatal("Error: --connect flag is required.\nUsage: --connect <IP address>")
	}

	// 네트워크 설정
	var netParams *chaincfg.Params
	var defaultPort string
	switch {
	case *signet:
		netParams = &chaincfg.SigNetParams
		defaultPort = "38333"
	case *testnet3:
		netParams = &chaincfg.TestNet3Params
		defaultPort = "18333"
	default:
		log.Fatal("Error: Please specify --signet or --testnet3")
	}

	// IP 및 포트 설정
	host, port, err := net.SplitHostPort(*connect)
	if err != nil {
		host = *connect
		port = defaultPort
	}

	if net.ParseIP(host) == nil {
		log.Fatal("Error: Invalid IP address.")
	}

	fullAddress := fmt.Sprintf("%s:%s", host, port)
	fmt.Printf("Connecting to node: %s\n", fullAddress)

	// 데이터베이스 경로 설정
	//Fast Filtered Level Data Base
	dbPath := filepath.Join(dataDir, "Blocks_ffldb")

	// 데이터베이스 없으면 생성
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		fmt.Println("Database not found. Creating new database...")
		db, err := database.Create("ffldb", dbPath, netParams.Net)
		if err != nil {
			log.Fatalf("Failed to create database: %v", err)
		}
		db.Close()
	}

	// 데이터베이스 열기
	db, err := database.Open("ffldb", dbPath, netParams.Net)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	var utreexo *blockchain.UtreexoViewpoint //utreexoview를 위해 정의
	utreexo = blockchain.NewUtreexoViewpoint()

	// Blockchain 초기화
	chain, err := blockchain.New(&blockchain.Config{
		DB:          db,
		ChainParams: netParams,
		TimeSource:  blockchain.NewMedianTime(),
		UtreexoView: utreexo,               // utreexoview를 만들어서 넣어야함, 어떻게 할 수 있을까, 만드는건 다른 레포지토리에서 하고 있으니 utreexod 라이브러리에 가서
		Checkpoints: netParams.Checkpoints, // 빠른 동기화, 안정성 등 장점밖에 없는데 그냥 기본적으로 작동되게 넣어두면 안되나?
		Interrupt:   nil,
	})
	if err != nil {
		log.Fatalf("Failed to create blockchain: %v", err)
	}
	log.Println("Blockchain initialized successfully!")

	// 노드 연결
	connectToNode(fullAddress, netParams, chain)
}

func connectToNode(nodeIP string, netParams *chaincfg.Params, chain *blockchain.BlockChain) {
	conn, err := net.Dial("tcp", nodeIP)
	if err != nil {
		log.Fatalf("Failed to connect to node: %v", err)
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.TCPAddr)
	remoteAddr := conn.RemoteAddr().(*net.TCPAddr)

	verMsg := wire.NewMsgVersion(
		wire.NewNetAddressIPPort(localAddr.IP, uint16(localAddr.Port), wire.SFNodeNetworkLimited|wire.SFNodeWitness),
		wire.NewNetAddressIPPort(remoteAddr.IP, uint16(remoteAddr.Port), 0),
		0,
		0,
	)

	err = wire.WriteMessage(conn, verMsg, wire.FeeFilterVersion, netParams.Net)
	if err != nil {
		log.Fatalf("Failed to send version message: %v", err)
	}

	for {
		msg, _, err := wire.ReadMessage(conn, wire.FeeFilterVersion, netParams.Net)
		if err != nil {
			log.Fatalf("Failed to read message: %v", err)
		}

		switch m := msg.(type) {
		case *wire.MsgVerAck:
			err = wire.WriteMessage(conn, wire.NewMsgVerAck(), wire.FeeFilterVersion, netParams.Net)
			if err != nil {
				log.Fatalf("Failed to send verack message: %v", err)
			}
			err = requestBlocks(conn, netParams, chain)
			if err != nil {
				log.Fatalf("Failed during block request: %v", err)
			}
			return
		default:
			fmt.Printf("Received other message: %T\n", m)
		}
	}
}

// 핸드쉐이크 완료 후 블록 요청 과정으로 블록 요청 하나하나 보는
// requestBlocks: 전체 흐름 관리
func requestBlocks(conn net.Conn, netParams *chaincfg.Params, chain *blockchain.BlockChain) error {
	targetBlockHash, err := chainhash.NewHashFromStr("000000bf4a1d3627b9ac861f795f2504650f05513198255a4b5de41102a03e15")
	if err != nil {
		return fmt.Errorf("Invalid target block hash: %v", err)
	}

	locator, err := chain.LatestBlockLocator()
	if err != nil {
		return err
	}

	// 초기 getblocks 요청
	err = sendGetBlocks(conn, netParams, locator, targetBlockHash)
	if err != nil {
		return err
	}

	// 메시지 처리 루프
	return processMessages(conn, netParams, chain, targetBlockHash)
}

// 블록체인에 유트렉스오뷰포인트를 넣고
// 인브메세지를 유트렉스오 블록으로 바꿈 msgblock,witnessblock, 하나 더
// sendGetBlocks: getblocks 메시지 전송/ MsgGetBlocks 메시지를 생성하고 전송. 초기 요청과 추가 요청에 재사용 가능
func sendGetBlocks(conn net.Conn, netParams *chaincfg.Params, blockLocator []*chainhash.Hash, targetBlockHash *chainhash.Hash) error {
	getBlocksMsg := &wire.MsgGetBlocks{
		ProtocolVersion:    wire.FeeFilterVersion,
		BlockLocatorHashes: blockLocator,
		HashStop:           *targetBlockHash,
	}
	err := wire.WriteMessage(conn, getBlocksMsg, wire.FeeFilterVersion, netParams.Net)
	if err != nil {
		return fmt.Errorf("Failed to send getblocks message: %v", err)
	}
	return nil
}

// processMessages: 메시지 수신 및 처리 루프/ 메시지 수신 루프를 관리. 각 메시지 타입에 맞는 핸들러 함수 호출.
func processMessages(conn net.Conn, netParams *chaincfg.Params, chain *blockchain.BlockChain, targetBlockHash *chainhash.Hash) error {
	blocksInQueue := make(map[chainhash.Hash]struct{}) //요청 중인 블록 해시를 추적하는거

	for {
		_, msg, bytes, err := wire.ReadMessageWithEncodingN(conn, wire.FeeFilterVersion, netParams.Net, wire.WitnessEncoding)
		// 메시지 읽음 연결,버전,네트워크종류,세그윗지원하는, 바이트는 메세지 바트
		if err != nil {
			log.Printf("Failed to read message: %v, %v", err, msg) // 에러 발생시 로그 남기고 진행
			continue
		}

		switch m := msg.(type) {
		case *wire.MsgInv: // MsgInv 처리/ 블록 해시를 포함하는 inv 메시지를 처리하고 getdata 요청 inventory는 Inventory vectors are used for notifying other nodes about objects they have or data which is being requested.
			err = handleInvMessage(m, chain, conn, netParams, blocksInQueue) // inv 메시지 처리 함수 호출                                          //500개 큐 받을때 뜸 에러,여기냐,에러2 순서로
			if err != nil {
				return err
			}

		case *wire.MsgBlock: // MsgBlock 처리 / 블록 메시지 처리하고 블록에 검증, 체인 업뎃, 목표 블록도 확인
			//MsgBlock은 Message 인터페이스를 구현하며 비트코인 블록 메시지를 나타냄, 주어진 블록 해시에 대한 getdata 메시지(MsgGetData)에 응답하여 블록 및 트랜잭션 정보를 전달하는 데 사용
			block, err := btcutil.NewBlockFromBytes(bytes)
			if err != nil {
				return err
			}
			err = handleBlockMessage(block, chain, blocksInQueue, targetBlockHash, conn, netParams) // 블록 메세지 처리 함수 호출
			if err != nil {
				return err
			}

		case *wire.MsgReject: // MsgReject 처리/ 거부 메세지를 처리하고 반환
			return handleRejectMessage(m)

		case *wire.MsgPing: // ping 메시지 처리 추가
			fmt.Println("Received ping, sending pong")
			pongMsg := wire.NewMsgPong(m.Nonce)
			err = wire.WriteMessage(conn, pongMsg, wire.FeeFilterVersion, netParams.Net)
			if err != nil {
				log.Printf("Failed to send pong: %v", err)
			}

		default:
			fmt.Printf("Other message: %T\n", m)
		}
	}
}

// handleInvMessage: MsgInv 처리/ getdata 요청을 보내고, 빈 InvList일 때 추가 getblocks 요청
func handleInvMessage(m *wire.MsgInv, chain *blockchain.BlockChain, conn net.Conn, netParams *chaincfg.Params, blocksInQueue map[chainhash.Hash]struct{}) error {
	getDataMsg := wire.NewMsgGetData()
	fmt.Println("creat getdata message") // getdata message 생성
	for _, inv := range m.InvList {      // inv 리스트 for룹
		//fmt.Println(inv.Hash.String())
		if inv.Type == wire.InvTypeBlock { // 만약 블록 타입이면
			fmt.Println("On Block Hash", inv.Hash.String()) // 현재 블록 해시 출력하고
			if chain.IsKnownOrphan(&inv.Hash) {
				continue
			}
			inv.Type = wire.InvTypeWitnessUtreexoBlock //지정하는것 InvWitnessBlock, InvTypeWitnessUtreexoBlock , 얘를 처음쓸지말지는 프로토콜 이해도에따라 다름, 그 다음 어떤거를 쓸지는 점프데프니션으로 알 수 있다.
			getDataMsg.AddInvVect(inv)                 // getdata 메시지에 현재 inv 벡터 추가 얘도 지우나마나 똑같고
			blocksInQueue[inv.Hash] = struct{}{}       // 대기 중인 블록 해시를 맵에 추가 근데 지우나 있으나 똑같은데?
		}
	}

	err := wire.WriteMessage(conn, getDataMsg, wire.FeeFilterVersion, netParams.Net) // getdata 메세지 전송
	if err != nil {                                                                  // 에러면 리턴 반환
		return fmt.Errorf("Failed to send getdata message: %v", err)
	}
	return nil // 에러 없으면 nil 반환
}

// handleBlockMessage: MsgBlock 처리/블록 검증, 체인 추가, 목표 블록 확인, 추가 요청 로직 포함, 수신된 블록 메시지를 처리하여 체인에 추가하고, 동기화 상태를 관리하며, 타겟 블록에 도달했는지 확인
func handleBlockMessage(block *btcutil.Block, chain *blockchain.BlockChain, blocksInQueue map[chainhash.Hash]struct{}, targetBlockHash *chainhash.Hash, conn net.Conn, netParams *chaincfg.Params) error {
	delete(blocksInQueue, *block.Hash()) // 현재 블록 해시를 대기 중인 블록 맵에서 제거

	isMainChain, _, err := chain.ProcessBlock(block, blockchain.BFNone)
	if err != nil {
		txs := block.Transactions()
		fmt.Println(txs[0].Hash())
		fmt.Printf("Block validation failed for %s: %v\n", block.Hash().String(), err)
		return nil
	}

	if !isMainChain {
		fmt.Printf("Received orphan block: %s, %v\n", block.Hash().String(), err) //block.Hahs는 바꾸기 스냅샤스로
		if len(blocksInQueue) == 0 {
			snapshot := chain.BestSnapshot()                                           // 대기 중인 블록이 없을 때 추가 getblocks 요청
			blockLocator := blockchain.BlockLocator([]*chainhash.Hash{&snapshot.Hash}) // 현재 블록 해시를 사용하여 블록 로케이터 생성
			// 스냅샷을 278라인에서 받고 281에 프로세스 블록을 지나면 현재 스냅샷이 달라지는데 왜 291에서 현재 스냅샷을 받아도 괜찮을까?
			getBlocksMsg := &wire.MsgGetBlocks{
				ProtocolVersion:    wire.FeeFilterVersion,
				BlockLocatorHashes: blockLocator,
				HashStop:           *targetBlockHash,
			}
			return wire.WriteMessage(conn, getBlocksMsg, wire.FeeFilterVersion, netParams.Net)
		}
	}

	if targetBlockHash.IsEqual(block.Hash()) {
		fmt.Println("Target block reached, exiting")
		utreexoView := chain.GetUtreexoView()                     // 현재 블록체인의 UtreexoViewpoint를 가져옴 utreexo set 그 자체, 머클 트리 걔
		fmt.Println("Utreexo Viewpoint:", utreexoView.ToString()) // Utreexo Viewpoint 출력
		conn.Close()
		os.Exit(0)
	}

	fmt.Println("Blocksinqueue", len(blocksInQueue)) // 현재 대기 중인 블록 수 출력 / 얘가 나와야하는데 286이 나옴
	if len(blocksInQueue) == 1 {
		for k, v := range blocksInQueue { // 대기 중인 블록 해시 출력
			fmt.Println(k, v)
		}
	}
	if len(blocksInQueue) == 0 { // 대기 중인 블록이 없을 때 추가 getblocks 요청
		blockLocator := blockchain.BlockLocator([]*chainhash.Hash{block.Hash()}) // 현재 블록 해시를 사용하여 블록 로케이터 생성
		getBlocksMsg := &wire.MsgGetBlocks{                                      // MsgGetBlocks 메세지 생성
			ProtocolVersion:    wire.FeeFilterVersion,
			BlockLocatorHashes: blockLocator,
			HashStop:           *targetBlockHash,
		}
		err = wire.WriteMessage(conn, getBlocksMsg, wire.FeeFilterVersion, netParams.Net)
		if err != nil {
			return fmt.Errorf("ailed to send next getblocks: %v", err)
		}
		fmt.Println("Sent additional getblocks request")
	}
	return nil
}

// handleRejectMessage: MsgReject 처리/거부 메시지를 출력하고 에러 반환
func handleRejectMessage(m *wire.MsgReject) error {
	fmt.Printf("Reject: %s\n", m.Reason)
	return fmt.Errorf("rejected: %s", m.Reason)
}

func downLoadUtreexoBlocks() {

}
