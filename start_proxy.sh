#!/bin/bash

export GOMAXPROCS=4
killall -9 rcproxy
make clean
make
nohup ./bin/rcproxy --debug-port=9876 &
