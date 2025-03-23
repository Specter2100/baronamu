package main

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
	dataDir := flag.String("datadir", "", "Directory to store data")
	flag.Parse()

	if *dataDir == "" {
		if runtime.GOOS == "windows" {
			*dataDir = "E:\\Bit\\활동\\코딩\\git\\utreexod\\cmd\\chaintipval" // Windows에서는 C 드라이브 사용
		} else {
			*dataDir = filepath.Join(os.Getenv("HOME"), ".utreexod") // Linux/macOS 기본 경로
		}
	}

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
		port = defaultPort
	}

	ipAddr := net.ParseIP(host)
	if ipAddr == nil {
		fmt.Println("Wrong IP Address.Try again")
		os.Exit(1)
	}

	fullAddress := fmt.Sprintf("%s:%s", host, port)
	fmt.Printf("Connecting to node: %s\n", fullAddress)

	// 데이터베이스 초기화??경로 설정// 데이터베이스 경로 설정
	dbPath := filepath.Join(*dataDir, "blocks_ffldb")

	// 데이터베이스 없으면 생성
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		fmt.Println("Database not found. Creating new database...")

		db, err := database.Create("ffldb", dbPath, netParams.Net)
		if err != nil {
			log.Fatalf("Failed to create database: %v", err)
		}
		db.Close()
	}

	// 데이터베이스 다시 열기
	db, err := database.Open("ffldb", dbPath, netParams.Net)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// UtreexoView 초기화
	//utreexoView := blockchain.NewUtreexoViewpoint() 97 쓸때

	// Blockchain 초기화
	chain, err := blockchain.New(&blockchain.Config{
		DB:          db,
		ChainParams: netParams,
		TimeSource:  blockchain.NewMedianTime(),
		UtreexoView: nil, //UtreexoView: utreexoView, 일반 노드로하고 나중에
		Checkpoints: netParams.Checkpoints,
		Interrupt:   nil,
	})
	if err != nil {
		log.Fatalf("Failed to create blockchain: %v", err)
		os.Exit(1) //오류가 생기면 비정상 종료 되도록
	}
	log.Println("Blockchain initialized successfully!") // 정상 실행 시 출력

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

	for {
		msg, _, err := wire.ReadMessage(conn, 0, netParams.Net)
		if err != nil {
			log.Fatalf("Failed to read message: %v", err)
		}

		switch m := msg.(type) {
		case *wire.MsgVerAck:
			fmt.Println("Received verack")
			verAckMsg := wire.NewMsgVerAck()
			err = wire.WriteMessage(conn, verAckMsg, 0, netParams.Net)
			if err != nil {
				log.Fatalf("Failed to send verack message: %v", err)
			}
			fmt.Println("Sent verack response")
			requestBlocks(conn, netParams, chain)
			return
		default:
			fmt.Printf("Received other message: %T\n", m)
		}
	}
}

func requestBlocks(conn net.Conn, netParams *chaincfg.Params, chain *blockchain.BlockChain) {
	genesisHash := netParams.GenesisHash
	// 수정: 블록체인 히스토리 기준으로 요청
	blockLocator := chain.BlockLocatorFromHash(genesisHash) // 체인 히스토리 생성
	getBlocksMsg := &wire.MsgGetBlocks{
		ProtocolVersion:    wire.ProtocolVersion,
		BlockLocatorHashes: blockLocator,
		HashStop:           chainhash.Hash{},
	}

	err := wire.WriteMessage(conn, getBlocksMsg, 0, netParams.Net)
	if err != nil {
		log.Fatalf("Failed to send getblocks message: %v", err)
	}
	fmt.Println("Sent getblocks request")

	requested := false

	for { //포룹이라 계속 반복
		msg, _, err := wire.ReadMessage(conn, 0, netParams.Net)
		if err != nil {
			if me, ok := err.(*wire.MessageError); ok && me.Description == "payload exceeds max length" {
				fmt.Println("Received invalid message (size limit exceeded), skipping:", err)
				return
			}
			log.Printf("Failed to read message: %v", err)
			continue
		}

		switch m := msg.(type) { //m은 무엇인가 m은 msg인데 이건 어디서 오는가가
		case *wire.MsgInv:
			if requested {
				fmt.Println("Ignoring additional MsgInv while waiting for MsgBlock")
				os.Exit(0)
			}
			fmt.Printf("Received inventory message: %d blocks available\n", len(m.InvList))
			getDataMsg := wire.NewMsgGetData() //데이터메세지 내가 생성하는거거
			for _, inv := range m.InvList {    //여기서 m이 어디서 오는가하면 186에서 오는거
				if inv.Type == wire.InvTypeBlock {
					getDataMsg.AddInvVect(inv)
				}
			}
			err = wire.WriteMessage(conn, getDataMsg, 0, netParams.Net)
			if err != nil {
				log.Printf("Failed to send getdata message: %v", err)
				return
			}
			fmt.Println("Sent getdata request for all blocks")
			requested = true
		case *wire.MsgBlock:
			fmt.Printf("Received block: %s, TxCount: %d\n",
				m.BlockHash().String(), len(m.Transactions))
			processBlock(m, chain)
			requested = false
		case *wire.MsgReject:
			fmt.Printf("Received reject message: Command=%s, Code=%d, Reason=%s\n",
				m.Cmd, m.Code, m.Reason)
			return
		default:
			fmt.Printf("Received other message: %T\n", m)
		}
	}
}

// orphan 블록을 저장할 맵
var orphanBlocks = make(map[chainhash.Hash]*wire.MsgBlock)

func processBlock(block *wire.MsgBlock, chain *blockchain.BlockChain) {
	blockHash := block.BlockHash()
	fmt.Println("Processing block:", blockHash.String())

	btcBlock := btcutil.NewBlock(block)
	_, isOrphan, err := chain.ProcessBlock(btcBlock, blockchain.BFNone)
	if err != nil {
		log.Printf("Failed to process block: %v", err)
		return
	}

	if isOrphan {
		fmt.Println("Received an orphan block, storing for later")
		orphanBlocks[blockHash] = block // Orphan block 저장
		return
	}

	fmt.Println("Block processed successfully")

	// 부모 블록이 있는 orphan 블록을 다시 처리
	retryOrphanBlocks(chain)
}
func retryOrphanBlocks(chain *blockchain.BlockChain) {
	for hash, orphan := range orphanBlocks {
		fmt.Println("Retrying orphan block:", hash.String())
		btcBlock := btcutil.NewBlock(orphan)
		_, isOrphan, err := chain.ProcessBlock(btcBlock, blockchain.BFNone)
		if err != nil {
			log.Printf("Failed to reprocess orphan block: %v", err)
			continue
		}

		if !isOrphan {
			fmt.Println("Orphan block successfully added to chain:", hash.String())
			delete(orphanBlocks, hash) // 정상 처리되면 orphan 목록에서 삭제
		}
	}
}
