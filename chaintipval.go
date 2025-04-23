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

// processMessages: 메시지 수신 및 처리 루프/ 메시지 수신 루프를 관리. 각 메시지 타입에 맞는 핸들러 함수 호출.
func processMessages(conn net.Conn, netParams *chaincfg.Params, chain *blockchain.BlockChain, targetBlockHash *chainhash.Hash) error {
	blocksInQueue := make(map[chainhash.Hash]struct{}) // 요청 중인 블록 해시를 추적

	for {
		fmt.Println("Waiting for message...")
		msg, _, err := wire.ReadMessage(conn, wire.ProtocolVersion, netParams.Net)
		if err != nil {
			log.Printf("Failed to read message: %v", err)
			continue
		}
		fmt.Printf("Received message: %T\n", msg)

		switch m := msg.(type) {
		case *wire.MsgInv:
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

		case *wire.MsgPing:
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
	fmt.Printf("MsgInv with %d items\n", len(m.InvList))
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
			BlockLocatorHashes: blockLocator,
			HashStop:           *targetBlockHash,
		}
		err := wire.WriteMessage(conn, getBlocksMsg, wire.ProtocolVersion, netParams.Net)
		if err != nil {
			return fmt.Errorf("failed to send additional getblocks: %v", err)
		}
		fmt.Println("Sent additional getblocks request")
		return nil
	}
	fmt.Printf("Sending getdata for %d blocks\n", len(getDataMsg.InvList))
	err := wire.WriteMessage(conn, getDataMsg, wire.ProtocolVersion, netParams.Net)
	if err != nil {
		return fmt.Errorf("failed to send getdata message: %v", err)
	}
	fmt.Println("Sent getdata request")
	return nil
}

// checkCoinbaseWitness: 코인베이스 트랜잭션의 witness 데이터 검증 및 디버깅 로그 출력
func checkCoinbaseWitness(block *btcutil.Block, netParams *chaincfg.Params) {
	fmt.Printf("Checking coinbase transaction for block %s\n", block.Hash().String())

	if len(block.Transactions()) == 0 {
		fmt.Println("Warning: No transactions in block")
		return
	}

	coinbaseTx := block.MsgBlock().Transactions[0]
	fmt.Printf("Coinbase tx outputs: %d\n", len(coinbaseTx.TxOut))
	fmt.Printf("Coinbase tx inputs: %d\n", len(coinbaseTx.TxIn))
	if len(coinbaseTx.TxIn) > 0 {
		fmt.Printf("Coinbase tx input[0] SignatureScript: %x\n", coinbaseTx.TxIn[0].SignatureScript)
		fmt.Printf("Coinbase tx input[0] PreviousOutPoint: %s\n", coinbaseTx.TxIn[0].PreviousOutPoint.String())
	}

	witnessCommitmentFound := false
	for i, out := range coinbaseTx.TxOut {
		fmt.Printf("Output %d: value=%v, scriptPubKey=%x\n", i, out.Value, out.PkScript)
		if len(out.PkScript) >= 38 && out.PkScript[0] == 0x6a && out.PkScript[1] == 0x24 {
			fmt.Printf("Witness commitment found: %x\n", out.PkScript[2:38])
			fmt.Printf("Debug: Witness commitment details - Prefix: aa21a9ed, Merkle Root: %x\n", out.PkScript[6:38])
			witnessCommitmentFound = true
		} else if out.PkScript[0] == 0x6a {
			asciiData := string(out.PkScript[2:])
			fmt.Printf("OP_RETURN data (ASCII): %s\n", asciiData)
			if len(out.PkScript[2:]) > 80 {
				fmt.Printf("Warning: OP_RETURN data exceeds 80 bytes: %d bytes\n", len(out.PkScript[2:]))
			}
		}
	}
	if !witnessCommitmentFound {
		fmt.Println("Warning: Witness commitment not found in coinbase transaction")
		fmt.Println("Debug: Non-SegWit block detected, may be accepted by legacy nodes")
	} else {
		fmt.Println("Debug: SegWit block detected, but witness stack may be non-standard due to soft fork")
	}

	if len(coinbaseTx.TxIn) > 0 {
		witnessStack := coinbaseTx.TxIn[0].Witness
		fmt.Printf("Coinbase tx witness stack: %v\n", witnessStack)
		if len(witnessStack) != 1 {
			fmt.Printf("Warning: Invalid witness stack size: %d (expected 1)\n", len(witnessStack))
			fmt.Printf("Debug: BIP 141 violation - Non-standard SegWit block, block %s\n", block.Hash().String())
			fmt.Printf("Debug: Coinbase tx details - Version: %d, LockTime: %d\n", coinbaseTx.Version, coinbaseTx.LockTime)
			f, _ := os.OpenFile("nonstd_blocks.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			defer f.Close()
			f.WriteString(fmt.Sprintf("Block %s: Invalid witness stack size %d\n", block.Hash().String(), len(witnessStack)))
		} else if len(witnessStack[0]) != 32 {
			fmt.Printf("Warning: Invalid witness stack item length: %d (expected 32 bytes)\n", len(witnessStack[0]))
		} else {
			fmt.Printf("Witness stack valid: %x\n", witnessStack[0])
		}
	} else {
		fmt.Println("Warning: Coinbase transaction has no inputs")
		fmt.Printf("Debug: BIP 141 violation - Non-standard SegWit block, block %s\n", block.Hash().String())
	}

	fmt.Printf("Debug: Pre-connection state for block %s, parent hash %s\n", block.Hash().String(), block.MsgBlock().Header.PrevBlock.String())
}

// handleBlockMessage: MsgBlock 처리/블록 검증, 체인 추가, 목표 블록 확인, 추가 요청 로직 포함, 수신된 블록 메시지를 처리하여 체인에 추가하고, 동기화 상태를 관리하며, 타겟 블록에 도달했는지 확인
func handleBlockMessage(m *wire.MsgBlock, chain *blockchain.BlockChain, blocksInQueue map[chainhash.Hash]struct{}, targetBlockHash *chainhash.Hash, conn net.Conn, netParams *chaincfg.Params) error {
	block := btcutil.NewBlock(m)
	delete(blocksInQueue, *block.Hash())
	snapshot := chain.BestSnapshot()
	fmt.Printf("best height %v, hash %v, got block %v\n", snapshot.Height, snapshot.Hash, block.Hash())

	// 코인베이스 트랜잭션의 Witness 데이터 검증 및 디버깅
	checkCoinbaseWitness(block, netParams)

	isMainChain, _, err := chain.ProcessBlock(block, blockchain.BFNone)
	if !isMainChain {
		fmt.Printf("Received orphan block: %s, %v\n", block.Hash().String(), err)
		parentHash := block.MsgBlock().Header.PrevBlock
		fmt.Printf("Orphan block's parent hash: %s\n", parentHash.String())
		getDataMsg := wire.NewMsgGetData()
		getDataMsg.AddInvVect(&wire.InvVect{Type: wire.InvTypeBlock, Hash: parentHash})
		err = wire.WriteMessage(conn, getDataMsg, wire.ProtocolVersion, netParams.Net)
		if err != nil {
			return fmt.Errorf("failed to request parent block: %v", err)
		}
		fmt.Printf("Requested parent block: %s\n", parentHash.String())
		blocksInQueue[parentHash] = struct{}{}
		return nil
	}

	if err != nil {
		fmt.Printf("block validation failed for %s: %v\n", block.Hash().String(), err)
		return nil
	}
	if targetBlockHash.IsEqual(block.Hash()) {
		fmt.Println("Target block reached, exiting")
		conn.Close()
		os.Exit(0)
	}

	fmt.Println("chain best height", chain.BestSnapshot().Height)
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
