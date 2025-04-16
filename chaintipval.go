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
			dataDir = "E:\\Bit\\활동\\코딩\\git\\utreexod\\cmd\\chaintipval"
		} else {
			//dataDir = filepath.Join(os.Getenv("HOME"), ".utreexod")
			panic("")
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

	// Blockchain 초기화
	chain, err := blockchain.New(&blockchain.Config{
		DB:          db,
		ChainParams: netParams,
		TimeSource:  blockchain.NewMedianTime(),
		UtreexoView: nil,
		Checkpoints: netParams.Checkpoints,
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
	fmt.Println("Connected to node:", nodeIP)

	localAddr := conn.LocalAddr().(*net.TCPAddr)
	remoteAddr := conn.RemoteAddr().(*net.TCPAddr)

	verMsg := wire.NewMsgVersion(
		wire.NewNetAddressIPPort(localAddr.IP, uint16(localAddr.Port), wire.SFNodeNetworkLimited),
		wire.NewNetAddressIPPort(remoteAddr.IP, uint16(remoteAddr.Port), 0),
		0,                         // nonce
		wire.SFNodeNetworkLimited, // services
	)

	err = wire.WriteMessage(conn, verMsg, wire.ProtocolVersion, netParams.Net)
	if err != nil {
		log.Fatalf("Failed to send version message: %v", err)
	}
	fmt.Println("Sent version message")

	for {
		msg, _, err := wire.ReadMessage(conn, wire.ProtocolVersion, netParams.Net)
		if err != nil {
			log.Fatalf("Failed to read message: %v", err)
		}

		switch m := msg.(type) {
		case *wire.MsgVerAck:
			fmt.Println("Received verack")
			err = wire.WriteMessage(conn, wire.NewMsgVerAck(), wire.ProtocolVersion, netParams.Net)
			if err != nil {
				log.Fatalf("Failed to send verack message: %v", err)
			}
			fmt.Println("Sent verack response")
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
	genesisHash := netParams.GenesisHash
	targetBlockHash, err := chainhash.NewHashFromStr("000000f02e5556ab63882bcc7f759223be5975ea7d8f2782cd7be0a5b7300b0c")
	if err != nil {
		return fmt.Errorf("invalid target block hash: %v", err)
	}

	// 타겟 블록의 높이를 확인 (디버깅용)
	targetHeight, err := chain.BlockHeightByHash(targetBlockHash)
	if err != nil {
		fmt.Printf("Target block %s height unknown: %v\n", targetBlockHash, err)
	} else {
		fmt.Printf("Target block %s is at height %d\n", targetBlockHash, targetHeight)
	}

	// 초기 설정
	currentHeight, blockLocator := setupBlockRequest(chain, genesisHash)
	fmt.Printf("Starting sync from height %d with locator %v\n", currentHeight, blockLocator)

	// 초기 getblocks 요청
	err = sendGetBlocks(conn, netParams, chain, blockLocator, targetBlockHash)
	if err != nil {
		return err
	}

	// 메시지 처리 루프
	return processMessages(conn, netParams, chain, targetBlockHash)
}

// setupBlockRequest: 초기 설정 (높이와 로케이터 준비)/ 현재 높이와 블록 로케이터를 준비해서 반환
func setupBlockRequest(chain *blockchain.BlockChain, genesisHash *chainhash.Hash) (int32, []*chainhash.Hash) {
	currentHeight := chain.BestSnapshot().Height
	fmt.Println("currentHeight", currentHeight)
	var blockLocator []*chainhash.Hash
	if currentHeight == 0 {
		blockLocator = chain.BlockLocatorFromHash(genesisHash)
	} else {
		blockLocator = chain.BlockLocatorFromHash(&chain.BestSnapshot().Hash)
	}
	return currentHeight, blockLocator
}

// sendGetBlocks: getblocks 메시지 전송/ MsgGetBlocks 메시지를 생성하고 전송. 초기 요청과 추가 요청에 재사용 가능
func sendGetBlocks(conn net.Conn, netParams *chaincfg.Params, chain *blockchain.BlockChain, blockLocator []*chainhash.Hash, targetBlockHash *chainhash.Hash) error {
	fmt.Println("Block locator:", blockLocator)
	getBlocksMsg := &wire.MsgGetBlocks{
		ProtocolVersion:    wire.ProtocolVersion,
		BlockLocatorHashes: blockLocator,
		HashStop:           *targetBlockHash,
	}
	err := wire.WriteMessage(conn, getBlocksMsg, wire.ProtocolVersion, netParams.Net)
	if err != nil {
		return fmt.Errorf("failed to send getblocks message: %v", err)
	}
	fmt.Println("Sent initial getblocks request")
	fmt.Println("chain best height", chain.BestSnapshot().Height)
	return nil
}

// processMessages: 메시지 수신 및 처리 루프/ 메시지 수신 루프를 관리. 각 메시지 타입에 맞는 핸들러 함수 호출. //여기서 걸리니 에러가 나오는거 아닌가 얘는 왜 processmessages를 go to def하면 중복되게 나오지??
func processMessages(conn net.Conn, netParams *chaincfg.Params, chain *blockchain.BlockChain, targetBlockHash *chainhash.Hash) error {
	blocksInQueue := make(map[chainhash.Hash]struct{}) //요청 중인 블록 해시를 추적하는거

	for {
		fmt.Println("Waiting for message...")                                      //500개 요청 보내고 대기
		msg, _, err := wire.ReadMessage(conn, wire.ProtocolVersion, netParams.Net) // 0 → wire.ProtocolVersion
		if err != nil {
			log.Printf("Failed to read message: %v", err)
			continue
		}
		fmt.Printf("Received message: %T\n", msg) //여기서 에러 메세지가 나오는데

		switch m := msg.(type) {
		case *wire.MsgInv: //invmessage 받는 경우 MsgInv 말하는거
			err = handleInvMessage(m, conn, netParams, blocksInQueue, chain, targetBlockHash)
			if err != nil {
				return err
			}

		case *wire.MsgBlock:
			err = handleBlockMessage(m, chain, blocksInQueue, targetBlockHash, conn, netParams)
			if err != nil {
				return err
			}

		case *wire.MsgReject:
			return handleRejectMessage(m)

		case *wire.MsgPing: // ping 메시지 처리 추가
			fmt.Println("Received ping, sending pong")
			pongMsg := wire.NewMsgPong(m.Nonce)
			err = wire.WriteMessage(conn, pongMsg, wire.ProtocolVersion, netParams.Net)
			if err != nil {
				log.Printf("Failed to send pong: %v", err)
			}

		default:
			fmt.Printf("Other message: %T\n", m)
		}
	}
}

// handleInvMessage: MsgInv 처리/ getdata 요청을 보내고, 빈 InvList일 때 추가 getblocks 요청
func handleInvMessage(m *wire.MsgInv, conn net.Conn, netParams *chaincfg.Params, blocksInQueue map[chainhash.Hash]struct{}, chain *blockchain.BlockChain, targetBlockHash *chainhash.Hash) error {
	fmt.Printf("MsgInv with %d items\n", len(m.InvList)) //추가 500개 요청하면 여기로 다시 넘어옴
	getDataMsg := wire.NewMsgGetData()
	for i, inv := range m.InvList {
		fmt.Printf(" - Item %d: %s\n", i, inv.Hash.String())
		if inv.Type == wire.InvTypeBlock {
			// 블록 높이를 확인해서 로그 출력
			height, err := chain.BlockHeightByHash(&inv.Hash)
			if err != nil {
				fmt.Printf(" - Block %s height unknown: %v\n", inv.Hash, err)
			} else {
				fmt.Printf(" - Block %s is at height %d\n", inv.Hash, height)
			}
			getDataMsg.AddInvVect(inv)
			blocksInQueue[inv.Hash] = struct{}{}
		}
	}
	if len(getDataMsg.InvList) == 0 {
		fmt.Println("Empty InvList, requesting more blocks")
		blockLocator := chain.BlockLocatorFromHash(&chain.BestSnapshot().Hash)
		getBlocksMsg := &wire.MsgGetBlocks{
			ProtocolVersion:    wire.ProtocolVersion,
			BlockLocatorHashes: blockLocator, //나한테서 체인팁을 요청함
			HashStop:           *targetBlockHash,
		}
		err := wire.WriteMessage(conn, getBlocksMsg, wire.ProtocolVersion, netParams.Net)
		if err != nil {
			return fmt.Errorf("failed to send additional getblocks: %v", err)
		}
		fmt.Println("500개 더 보내라고 요청.Sent additional getblocks request") //500개 받고 나면 여기서 추가로 블록 요청한다고 보냄
		return nil
	}
	fmt.Printf("Sending getdata for %d blocks\n", len(getDataMsg.InvList)) //5뭐였지
	err := wire.WriteMessage(conn, getDataMsg, wire.ProtocolVersion, netParams.Net)
	if err != nil {
		return fmt.Errorf("failed to send getdata message: %v", err)
	}
	fmt.Println("Sent getdata request")
	return nil
}

// handleBlockMessage: MsgBlock 처리/블록 검증, 체인 추가, 목표 블록 확인, 추가 요청 로직 포함, 수신된 블록 메시지를 처리하여 체인에 추가하고, 동기화 상태를 관리하며, 타겟 블록에 도달했는지 확인
func handleBlockMessage(m *wire.MsgBlock, chain *blockchain.BlockChain, blocksInQueue map[chainhash.Hash]struct{}, targetBlockHash *chainhash.Hash, conn net.Conn, netParams *chaincfg.Params) error {
	block := btcutil.NewBlock(m)
	delete(blocksInQueue, *block.Hash())
	snapshot := chain.BestSnapshot()                      //로컬 한테서 snapshot을 찍는건데 여기서는 블록체인 패키지를 가져온거인데 최고 높이를 찍는거
	fmt.Printf("best height %v, hash %v, got block %v\n", // 얘가 wire.MsgBlock 다음 나오는 메세지
		snapshot.Height, snapshot.Hash, block.Hash())
	isMainChain, _, err := chain.ProcessBlock(block, blockchain.BFNone) //processblock 은 블록체인에 새로운 블록을 추가하는 주요 함수
	if !isMainChain {                                                   // err면 실행==메인 체인에 연결 안되면 참, 여기가 연결 실패 구분하는 첫 단계인가/ chain.ProcessBlock의 반환값으로, 블록이 메인 체인에 추가되었는지 타나냄
		fmt.Printf("또 Received orphan block: %s, %v\n", block.Hash().String(), err)
		parentHash := block.MsgBlock().Header.PrevBlock                     //부모 블록해시를 정의하는 변수수
		fmt.Printf("Orphan block's parent hash: %s\n", parentHash.String()) //고아 블록의 부모 블록이 뭔지 확인하는 코드 추가
		//	os.Exit(0)                                                          // 나중에 삭제
		getDataMsg := wire.NewMsgGetData()                                              //새로운 getdatamsg 요청
		getDataMsg.AddInvVect(&wire.InvVect{Type: wire.InvTypeBlock, Hash: parentHash}) //블록데이터 요청, 부모 해시 부모 블록을 요청에 추가한다고? durltj typeblock 말고 InvTypeFilteredBlock쓰면 안되나? 로그 보기도 더 쉬운데
		err = wire.WriteMessage(conn, getDataMsg, wire.ProtocolVersion, netParams.Net)  //피어에게 위에 메세지 달라고 요청
		if err != nil {
			return fmt.Errorf("failed to request parent block: %v", err)
		}
		fmt.Printf("Requested parent block: %s\n", parentHash.String())
		blocksInQueue[parentHash] = struct{}{}
		return nil
	}

	if err != nil {
		fmt.Printf("block validation failed for %s: %v\n", block.Hash().String(), err)
		return nil //에러면 nil 반환하고 종료
	}
	if targetBlockHash.IsEqual(block.Hash()) {
		fmt.Println("Target block reached, exiting")
		conn.Close()
		os.Exit(0)
	}

	fmt.Println("chain best height", chain.BestSnapshot().Height) //얘도 없어도 되겠는데데
	fmt.Println("blocks in queue", len(blocksInQueue))
	if len(blocksInQueue) == 0 {
		blockLocator := blockchain.BlockLocator([]*chainhash.Hash{block.Hash()})
		fmt.Printf("locator %v, target %v\n", block.Hash(), targetBlockHash)
		getBlocksMsg := &wire.MsgGetBlocks{
			ProtocolVersion:    wire.ProtocolVersion,
			BlockLocatorHashes: blockLocator,
			HashStop:           *targetBlockHash,
		}
		err = wire.WriteMessage(conn, getBlocksMsg, wire.ProtocolVersion, netParams.Net)
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
