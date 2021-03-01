/*
Copyright Greg Haskins <gregory.haskins@gmail.com> 2017, All Rights Reserved.
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

func dirExists(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	return fi.IsDir()
}

//AddConfigPath 添加配置文件绝对路径到viper.configpaths数组中
func AddConfigPath(v *viper.Viper, p string) {
	if v != nil { //是存在的 一开始new新的viper 有默认值
		v.AddConfigPath(p)
	} else {
		viper.AddConfigPath(p)
	}
}

//----------------------------------------------------------------------------------
// TranslatePath()
//----------------------------------------------------------------------------------
// Translates a relative path into a fully qualified path relative to the config
// file that specified it.  Absolute paths are passed unscathed.
//----------------------------------------------------------------------------------
func TranslatePath(base, p string) string {
	if filepath.IsAbs(p) {
		return p
	}

	return filepath.Join(base, p)
}

//----------------------------------------------------------------------------------
// TranslatePathInPlace()
//----------------------------------------------------------------------------------
// Translates a relative path into a fully qualified path in-place (updating the
// pointer) relative to the config file that specified it.  Absolute paths are
// passed unscathed.
//----------------------------------------------------------------------------------
func TranslatePathInPlace(base string, p *string) {
	*p = TranslatePath(base, *p)
}

//----------------------------------------------------------------------------------
// GetPath()
//----------------------------------------------------------------------------------
// GetPath allows configuration strings that specify a (config-file) relative path
//
// For example: Assume our config is located in /etc/hyperledger/fabric/core.yaml with
// a key "msp.configPath" = "msp/config.yaml".
//
// This function will return:
//      GetPath("msp.configPath") -> /etc/hyperledger/fabric/msp/config.yaml
//
//----------------------------------------------------------------------------------
func GetPath(key string) string {
	p := viper.GetString(key)
	if p == "" {
		return ""
	}

	return TranslatePath(filepath.Dir(viper.ConfigFileUsed()), p)
}

const OfficialPath = "/etc/hyperledger/fabric"

//----------------------------------------------------------------------------------
// InitViper()
//----------------------------------------------------------------------------------
// Performs basic initialization of our viper-based configuration layer.
// Primary thrust is to establish the paths that should be consulted to find
// the configuration we need.  If v == nil, we will initialize the global
// Viper instance
//----------------------------------------------------------------------------------
func InitViper(v *viper.Viper, configName string) error {
	var altPath = os.Getenv("FABRIC_CFG_PATH") //获取环境变量值 即配置文件路径
	if altPath != "" {
		// If the user has overridden the path with an envvar, its the only path
		// we will consider

		if !dirExists(altPath) {
			return fmt.Errorf("FABRIC_CFG_PATH %s does not exist", altPath)
		}

		AddConfigPath(v, altPath) //添加配置文件绝对路径到viper.configpaths数组中
	} else {
		// If we get here, we should use the default paths in priority order:
		//
		// *) CWD
		// *) /etc/hyperledger/fabric

		// CWD
		AddConfigPath(v, "./")

		// And finally, the official path
		if dirExists(OfficialPath) {
			AddConfigPath(v, OfficialPath)
		}
	}

	// Now set the configuration file.
	if v != nil {
		v.SetConfigName(configName) //设置 配置文件名称
	} else {
		viper.SetConfigName(configName)
	}

	return nil
}
