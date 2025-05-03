package main

// 노드와 연결-블록 받기-검증-시스템 종료료
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
	dbPath := filepath.Join(dataDir, "blocks_ffldb")

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
		UtreexoView: utreexo, //utreexoview를 만들어서 넣어야함, 어떻게 할 수 있을까, 만드는건 다른 레포지토리에서 하고 있으니 utreexod 라이브러리에 가서
		//Checkpoints: netParams.Checkpoints,
		Interrupt: nil,
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
		0, // nonce
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
	targetBlockHash, err := chainhash.NewHashFromStr("00000002d38fc984fa25a057930af276c00a001428bd68b8216f826d580a382f")
	if err != nil {
		return fmt.Errorf("invalid target block hash: %v", err)
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

// sendGetBlocks: getblocks 메시지 전송/ MsgGetBlocks 메시지를 생성하고 전송. 초기 요청과 추가 요청에 재사용 가능
func sendGetBlocks(conn net.Conn, netParams *chaincfg.Params, blockLocator []*chainhash.Hash, targetBlockHash *chainhash.Hash) error {
	getBlocksMsg := &wire.MsgGetBlocks{
		ProtocolVersion:    wire.FeeFilterVersion,
		BlockLocatorHashes: blockLocator,
		HashStop:           *targetBlockHash,
	}
	err := wire.WriteMessage(conn, getBlocksMsg, wire.FeeFilterVersion, netParams.Net)
	if err != nil {
		return fmt.Errorf("failed to send getblocks message: %v", err)
	}
	return nil
}

// processMessages: 메시지 수신 및 처리 루프/ 메시지 수신 루프를 관리. 각 메시지 타입에 맞는 핸들러 함수 호출. //여기서 걸리니 에러가 나오는거 아닌가 얘는 왜 processmessages를 go to def하면 중복되게 나오지??
func processMessages(conn net.Conn, netParams *chaincfg.Params, chain *blockchain.BlockChain, targetBlockHash *chainhash.Hash) error {
	blocksInQueue := make(map[chainhash.Hash]struct{}) //요청 중인 블록 해시를 추적하는거

	for {
		_, msg, bytes, err := wire.ReadMessageWithEncodingN(conn, wire.FeeFilterVersion, netParams.Net, wire.WitnessEncoding) // 0 → wire.ProtocolVersion
		if err != nil {
			log.Printf("Failed to read message: %v, %v", err, msg)
			continue
		}

		switch m := msg.(type) {
		case *wire.MsgInv: //invmessage 받는 경우 MsgInv 말하는거
			err = handleInvMessage(m, chain, conn, netParams, blocksInQueue)
			if err != nil {
				return err
			}

		case *wire.MsgBlock:
			block, err := btcutil.NewBlockFromBytes(bytes)
			if err != nil {
				return err
			}
			err = handleBlockMessage(block, chain, blocksInQueue, targetBlockHash, conn, netParams)
			if err != nil {
				return err
			}

		case *wire.MsgReject:
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
	for _, inv := range m.InvList {
		if inv.Type == wire.InvTypeBlock {
			fmt.Println("on item", inv.Hash.String())
			if chain.IsKnownOrphan(&inv.Hash) {
				continue
			}
			inv.Type = wire.InvTypeWitnessBlock
			getDataMsg.AddInvVect(inv)
			blocksInQueue[inv.Hash] = struct{}{}
		}
	}

	err := wire.WriteMessage(conn, getDataMsg, wire.FeeFilterVersion, netParams.Net)
	if err != nil {
		return fmt.Errorf("failed to send getdata message: %v", err)
	}
	return nil
}

// handleBlockMessage: MsgBlock 처리/블록 검증, 체인 추가, 목표 블록 확인, 추가 요청 로직 포함, 수신된 블록 메시지를 처리하여 체인에 추가하고, 동기화 상태를 관리하며, 타겟 블록에 도달했는지 확인
func handleBlockMessage(block *btcutil.Block, chain *blockchain.BlockChain, blocksInQueue map[chainhash.Hash]struct{}, targetBlockHash *chainhash.Hash, conn net.Conn, netParams *chaincfg.Params) error {
	delete(blocksInQueue, *block.Hash())
	snapshot := chain.BestSnapshot()
	fmt.Printf("best height %v, hash %v, got block %v\n", snapshot.Height, snapshot.Hash, block.Hash())

	isMainChain, _, err := chain.ProcessBlock(block, blockchain.BFNone)
	if err != nil {
		txs := block.Transactions()
		fmt.Println(txs[0].Hash())
		fmt.Printf("block validation failed for %s: %v\n", block.Hash().String(), err)
		return nil
	}
	if !isMainChain {
		fmt.Printf("Received orphan block: %s, %v\n", block.Hash().String(), err)
		return nil
	}

	if targetBlockHash.IsEqual(block.Hash()) {
		fmt.Println("Target block reached, exiting")
		conn.Close()
		os.Exit(0)
	}

	fmt.Println("blocksinqueue", len(blocksInQueue))
	if len(blocksInQueue) == 1 {
		for k, v := range blocksInQueue {
			fmt.Println(k, v)
		}
	}
	if len(blocksInQueue) == 0 {
		blockLocator := blockchain.BlockLocator([]*chainhash.Hash{block.Hash()})
		getBlocksMsg := &wire.MsgGetBlocks{
			ProtocolVersion:    wire.FeeFilterVersion,
			BlockLocatorHashes: blockLocator,
			HashStop:           *targetBlockHash,
		}
		err = wire.WriteMessage(conn, getBlocksMsg, wire.FeeFilterVersion, netParams.Net)
		if err != nil {
			return fmt.Errorf("failed to send next getblocks: %v", err)
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
