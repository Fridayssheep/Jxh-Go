package database

import (
	"errors"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/zjutjh/jxh-go/internal/platform/config"
)

var databaseCharset = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

func buildDriverConfig(cfg config.DatabaseConfig) (*drivermysql.Config, error) {
	var driverConfig *drivermysql.Config
	var err error
	if cfg.DSN != "" {
		driverConfig, err = drivermysql.ParseDSN(cfg.DSN)
		if err != nil {
			return nil, errors.New("parse database configuration: invalid DSN")
		}
	} else {
		location, err := time.LoadLocation(cfg.Loc)
		if err != nil {
			return nil, errors.New("load database location: invalid location")
		}
		driverConfig = drivermysql.NewConfig()
		driverConfig.User = cfg.User
		driverConfig.Passwd = cfg.Password
		driverConfig.Net = "tcp"
		driverConfig.Addr = net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
		driverConfig.DBName = cfg.Name
		driverConfig.ParseTime = cfg.ParseTime
		driverConfig.Loc = location
		if !databaseCharset.MatchString(cfg.Charset) {
			return nil, errors.New("apply database charset: invalid charset")
		}
		if err := driverConfig.Apply(drivermysql.Charset(cfg.Charset, "")); err != nil {
			return nil, errors.New("apply database charset: invalid charset")
		}
	}
	if driverConfig.Timeout <= 0 {
		driverConfig.Timeout = 5 * time.Second
	}
	if err := validateDSNIdentifiers(driverConfig.FormatDSN()); err != nil {
		return nil, err
	}
	return driverConfig, nil
}

func validateDSNIdentifiers(dsn string) error {
	queryAt := strings.LastIndexByte(dsn, '?')
	if queryAt < 0 {
		return nil
	}
	params, err := url.ParseQuery(dsn[queryAt+1:])
	if err != nil {
		return errors.New("parse database configuration: invalid DSN")
	}
	for _, charsets := range params["charset"] {
		for charset := range strings.SplitSeq(charsets, ",") {
			if !databaseCharset.MatchString(charset) {
				return errors.New("apply database charset: invalid charset")
			}
		}
	}
	for _, collation := range params["collation"] {
		if !databaseCharset.MatchString(collation) {
			return errors.New("apply database collation: invalid collation")
		}
	}
	return nil
}
