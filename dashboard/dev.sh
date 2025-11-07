#!/bin/bash
# Development helper script for the test results dashboard

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}TrueNAS CSI Dashboard Development Helper${NC}"
echo "========================================"

# Check if Node.js is installed
if ! command -v node &> /dev/null; then
    echo -e "${RED}Error: Node.js is not installed. Please install Node.js 18+ first.${NC}"
    exit 1
fi

# Check Node.js version
NODE_VERSION=$(node --version | sed 's/v//')
REQUIRED_VERSION="18.0.0"
if ! [ "$(printf '%s\n' "$REQUIRED_VERSION" "$NODE_VERSION" | sort -V | head -n1)" = "$REQUIRED_VERSION" ]; then
    echo -e "${RED}Error: Node.js version $NODE_VERSION is too old. Please upgrade to Node.js 18+.${NC}"
    exit 1
fi

echo -e "${GREEN}✓ Node.js $NODE_VERSION detected${NC}"

# Install dependencies if needed
if [ ! -d "node_modules" ]; then
    echo -e "${YELLOW}Installing dependencies...${NC}"
    npm install
    echo -e "${GREEN}✓ Dependencies installed${NC}"
else
    echo -e "${GREEN}✓ Dependencies already installed${NC}"
fi

# Check for GitHub token
if [ -z "$GITHUB_TOKEN" ]; then
    echo -e "${YELLOW}Warning: GITHUB_TOKEN not set. Dashboard generation may fail.${NC}"
    echo -e "${YELLOW}Set it with: export GITHUB_TOKEN=your_token_here${NC}"
fi

case "${1:-help}" in
    "build")
        echo -e "${BLUE}Building dashboard...${NC}"
        npm run build
        echo -e "${GREEN}✓ Dashboard built successfully${NC}"
        echo -e "${BLUE}Output: dist/index.html${NC}"
        ;;

    "serve")
        echo -e "${BLUE}Starting development server...${NC}"
        echo -e "${BLUE}Dashboard will be available at: http://localhost:3000${NC}"
        echo -e "${YELLOW}Press Ctrl+C to stop${NC}"
        npm run dev
        ;;

    "test")
        echo -e "${BLUE}Running tests...${NC}"
        npm test
        echo -e "${GREEN}✓ Tests passed${NC}"
        ;;

    "clean")
        echo -e "${BLUE}Cleaning build artifacts...${NC}"
        rm -rf dist node_modules package-lock.json
        echo -e "${GREEN}✓ Cleaned${NC}"
        ;;

    "help"|*)
        echo "Usage: $0 [command]"
        echo ""
        echo "Commands:"
        echo "  build    Generate the dashboard"
        echo "  serve    Start development server"
        echo "  test     Run tests"
        echo "  clean    Clean build artifacts"
        echo "  help     Show this help message"
        echo ""
        echo "Environment variables:"
        echo "  GITHUB_TOKEN    GitHub personal access token (required for API access)"
        ;;
esac