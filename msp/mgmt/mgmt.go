/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package mgmt

import (
	"reflect"
	"sync"

	"github.com/hyperledger/fabric/bccsp/factory"
	"github.com/hyperledger/fabric/common/flogging"
	"github.com/hyperledger/fabric/msp"
	"github.com/hyperledger/fabric/msp/cache"
	"github.com/pkg/errors"
	"github.com/spf13/viper"
)

// LoadLocalMspWithType loads the local MSP with the specified type from the specified directory
func LoadLocalMspWithType(dir string, bccspConfig *factory.FactoryOpts, mspID, mspType string) error {
	if mspID == "" {
		return errors.New("the local MSP must have an ID")
	}

	conf, err := msp.GetLocalMspConfigWithType(dir, bccspConfig, mspID, mspType) //根据mspType(默认bsscp)获取指定路径msp
	if err != nil {
		return err
	}

	return GetLocalMSP().Setup(conf) //根据conf内容 构建本节点msp管理
}

// LoadLocalMsp loads the local MSP from the specified directory
func LoadLocalMsp(dir string, bccspConfig *factory.FactoryOpts, mspID string) error {
	if mspID == "" {
		return errors.New("the local MSP must have an ID")
	}

	conf, err := msp.GetLocalMspConfig(dir, bccspConfig, mspID) //获取msp目录下所有信息 信息赋值结构 并返回msp结构
	if err != nil {
		return err
	}

	return GetLocalMSP().Setup(conf) //setup==> ../mspimpl.go No.222 line
}

// FIXME: AS SOON AS THE CHAIN MANAGEMENT CODE IS COMPLETE,
// THESE MAPS AND HELPSER FUNCTIONS SHOULD DISAPPEAR BECAUSE
// OWNERSHIP OF PER-CHAIN MSP MANAGERS WILL BE HANDLED BY IT;
// HOWEVER IN THE INTERIM, THESE HELPER FUNCTIONS ARE REQUIRED

var m sync.Mutex
var localMsp msp.MSP
var mspMap map[string]msp.MSPManager = make(map[string]msp.MSPManager)
var mspLogger = flogging.MustGetLogger("msp")

// TODO - this is a temporary solution to allow the peer to track whether the
// MSPManager has been setup for a channel, which indicates whether the channel
// exists or not
type mspMgmtMgr struct {
	msp.MSPManager
	// track whether this MSPManager has been setup successfully
	up bool
}

func (mgr *mspMgmtMgr) DeserializeIdentity(serializedIdentity []byte) (msp.Identity, error) {
	if !mgr.up {
		return nil, errors.New("channel doesn't exist")
	}
	return mgr.MSPManager.DeserializeIdentity(serializedIdentity)
}

func (mgr *mspMgmtMgr) Setup(msps []msp.MSP) error {
	err := mgr.MSPManager.Setup(msps)
	if err == nil {
		mgr.up = true
	}
	return err
}

// GetManagerForChain returns the msp manager for the supplied
// chain; if no such manager exists, one is created
func GetManagerForChain(chainID string) msp.MSPManager {
	m.Lock()
	defer m.Unlock()

	mspMgr, ok := mspMap[chainID]
	if !ok {
		mspLogger.Debugf("Created new msp manager for channel `%s`", chainID)
		mspMgmtMgr := &mspMgmtMgr{msp.NewMSPManager(), false}
		mspMap[chainID] = mspMgmtMgr
		mspMgr = mspMgmtMgr
	} else {
		// check for internal mspManagerImpl and mspMgmtMgr types. if a different
		// type is found, it's because a developer has added a new type that
		// implements the MSPManager interface and should add a case to the logic
		// above to handle it.
		if !(reflect.TypeOf(mspMgr).Elem().Name() == "mspManagerImpl" || reflect.TypeOf(mspMgr).Elem().Name() == "mspMgmtMgr") {
			panic("Found unexpected MSPManager type.")
		}
		mspLogger.Debugf("Returning existing manager for channel '%s'", chainID)
	}
	return mspMgr
}

// GetManagers returns all the managers registered
func GetDeserializers() map[string]msp.IdentityDeserializer {
	m.Lock()
	defer m.Unlock()

	clone := make(map[string]msp.IdentityDeserializer)

	for key, mspManager := range mspMap {
		clone[key] = mspManager
	}

	return clone
}

// XXXSetMSPManager is a stopgap solution to transition from the custom MSP config block
// parsing to the channelconfig.Resources interface, while preserving the problematic singleton
// nature of the MSP manager
func XXXSetMSPManager(chainID string, manager msp.MSPManager) {
	m.Lock()
	defer m.Unlock()

	mspMap[chainID] = &mspMgmtMgr{manager, true}
}

// GetLocalMSP returns the local msp (and creates it if it doesn't exist)
func GetLocalMSP() msp.MSP {
	m.Lock()
	defer m.Unlock()

	if localMsp != nil {
		return localMsp
	}

	localMsp = loadLocaMSP()

	return localMsp
}

func loadLocaMSP() msp.MSP {
	// determine the type of MSP (by default, we'll use bccspMSP)
	mspType := viper.GetString("peer.localMspType") //如果viper中有则获取 没有赋值默认 mspType==bccsp
	if mspType == "" {
		mspType = msp.ProviderTypeToString(msp.FABRIC) //mspType=bccsp
	}

	var mspOpts = map[string]msp.NewOpts{ //类型:版本 以类型（bccsp）为key
		msp.ProviderTypeToString(msp.FABRIC): &msp.BCCSPNewOpts{NewBaseOpts: msp.NewBaseOpts{Version: msp.MSPv1_4_3}},
		msp.ProviderTypeToString(msp.IDEMIX): &msp.IdemixNewOpts{NewBaseOpts: msp.NewBaseOpts{Version: msp.MSPv1_1}},
	}
	newOpts, found := mspOpts[mspType]
	if !found {
		mspLogger.Panicf("msp type " + mspType + " unknown")
	}

	mspInst, err := msp.New(newOpts) //根据类型和版本 构建msp实例 可以生成证书密钥 身份和签名身份
	if err != nil {
		mspLogger.Fatalf("Failed to initialize local MSP, received err %+v", err)
	}
	switch mspType {
	case msp.ProviderTypeToString(msp.FABRIC): //默认msptype = bsscp
		mspInst, err = cache.New(mspInst)
		if err != nil {
			mspLogger.Fatalf("Failed to initialize local MSP, received err %+v", err)
		}
	case msp.ProviderTypeToString(msp.IDEMIX):
		// Do nothing
	default:
		panic("msp type " + mspType + " unknown")
	}

	mspLogger.Debugf("Created new local MSP")

	return mspInst
}

// GetIdentityDeserializer returns the IdentityDeserializer for the given chain
func GetIdentityDeserializer(chainID string) msp.IdentityDeserializer {
	if chainID == "" {
		return GetLocalMSP()
	}

	return GetManagerForChain(chainID)
}

// GetLocalSigningIdentityOrPanic returns the local signing identity or panic in case
// or error
func GetLocalSigningIdentityOrPanic() msp.SigningIdentity {
	id, err := GetLocalMSP().GetDefaultSigningIdentity() //GetDefaultSigningIdentity()实现 fabric\msp\mspimpl.go No.269 line
	if err != nil {
		mspLogger.Panicf("Failed getting local signing identity [%+v]", err)
	}
	return id
}
