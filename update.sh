#!/bin/bash
sed -i '' 's/DRStore        \*dr.DRStore//g' agent/agent.go
sed -i '' 's/"github.com\/nousresearch\/hermes-agent\/agent-comm\/dr"//g' agent/agent.go
