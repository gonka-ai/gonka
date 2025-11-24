#!/usr/bin/env python3
"""
Gonka AI Chat Terminal - Interactive Demo
==========================================
A beautiful terminal-based chat interface showcasing Gonka AI's capabilities.
Perfect for onboarding and demonstrations!

Features:
- Real-time streaming responses with plain text formatting
- Chat history preservation
- Optional conversation logging
- Performance metrics (TTFT, throughput)
- Clean, intuitive UX
"""

import os
import sys
import argparse
import logging
import subprocess
import time
from datetime import datetime
from pathlib import Path

from gonka_openai import GonkaOpenAI
from rich.console import Console
from rich.panel import Panel
from rich.text import Text

# =============================================================================
# CONFIGURATION
# =============================================================================

# Repository context configuration
MAX_CONTEXT_CHARS = 20000  # Maximum total characters to load from docs
MAX_FILE_CHARS = 3000      # Maximum characters per file
CONTEXT_FILES = [
    'README.md',
    'CONTRIBUTING.md',
    'docs/consumer_setup.md',
    'docs/tokenomics.md',
]

# Model configuration
DEFAULT_MODEL = "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8"
DEFAULT_TEMPERATURE = 0.2  # Low temperature for factual, deterministic responses

# =============================================================================
# INITIALIZATION
# =============================================================================

def clear_screen():
    """Clear the terminal screen for a fresh start."""
    try:
        subprocess.run("clear" if os.name != "nt" else "cls", shell=True)
    except Exception:
        pass

clear_screen()

# Suppress verbose logging from HTTP libraries
logging.getLogger("httpx").setLevel(logging.ERROR)
logging.getLogger("openai").setLevel(logging.ERROR)
logging.getLogger("urllib3").setLevel(logging.ERROR)
logging.getLogger().setLevel(logging.ERROR)

# =============================================================================
# CLI CONFIGURATION
# =============================================================================

parser = argparse.ArgumentParser(
    description="🚀 Gonka AI Chat Terminal - Experience the power of decentralized AI",
    formatter_class=argparse.RawDescriptionHelpFormatter,
    epilog="""
Examples:
  python gonka_chat_cli.py              # Start interactive chat
  python gonka_chat_cli.py --save       # Save conversation to log file
    """
)
parser.add_argument(
    "--save",
    action="store_true",
    help="Save complete chat history to a timestamped log file"
)
args = parser.parse_args()

# =============================================================================
# LOGGING SETUP
# =============================================================================

timestamp = datetime.now().strftime("%Y-%m-%d_%H-%M-%S")
logfile = f"gonka_chat_{timestamp}.log" if args.save else None

if args.save:
    logging.basicConfig(
        filename=logfile,
        level=logging.INFO,
        format="%(asctime)s [%(levelname)s] %(message)s"
    )

# =============================================================================
# CONSOLE & ENVIRONMENT
# =============================================================================

console = Console()

# Load environment configuration
GONKA_KEY = os.getenv("GONKA_PRIVATE_KEY")
NODE_URL = os.getenv("NODE_URL", "http://node1.gonka.ai:8000")
MODEL_NAME = os.getenv("MODEL_NAME", DEFAULT_MODEL)

# Validate required credentials
if not GONKA_KEY:
    console.print(Panel(
        "[red]❌ Missing GONKA_PRIVATE_KEY environment variable.[/red]\n\n"
        "[yellow]Please set your Gonka API key:[/yellow]\n"
        "[cyan]export GONKA_PRIVATE_KEY='your-key-here'[/cyan]",
        title="⚠️  Configuration Error",
        border_style="red"
    ))
    sys.exit(1)

# =============================================================================
# GONKA CLIENT INITIALIZATION
# =============================================================================

console.print("[dim]Initializing Gonka AI client...[/dim]")

client = GonkaOpenAI(
    gonka_private_key=GONKA_KEY,
    source_url=NODE_URL
)

chat_history = []

# =============================================================================
# LOAD REPOSITORY CONTEXT
# =============================================================================

repo_context = None
repo_root = Path.cwd()

if (repo_root / ".git").exists():
    console.print("[dim]📚 Loading markdown files from repository...[/dim]")

    try:
        context_parts = []
        total_chars = 0

        for file_path in CONTEXT_FILES:
            full_path = repo_root / file_path
            if not full_path.exists():
                continue

            if total_chars >= MAX_CONTEXT_CHARS:
                break

            try:
                content = full_path.read_text(encoding="utf-8", errors="ignore")

                # Limit each file
                if len(content) > MAX_FILE_CHARS:
                    content = content[:MAX_FILE_CHARS] + "\n... [truncated]"

                context_parts.append(f"## File: {file_path}\n\n{content}")
                total_chars += len(content)
            except Exception:
                continue

        if context_parts:
            all_content = "\n\n---\n\n".join(context_parts)
            repo_context = f"""You are a helpful assistant for Gonka AI. Answer questions using ONLY the documentation below.

{all_content}

---

Rules:
- Answer ONLY from what's written above - do not assume, extrapolate, or guess
- ALWAYS include actual command lines, code examples, and API calls when they appear in the docs
- Show complete step-by-step instructions with real commands from the documentation
- When README.md points to https://gonka.ai/ links, direct users there without elaborating
- Do NOT invent: commands, API endpoints, file paths, procedures, or examples not in docs
- ONLY cite files explicitly shown above (check "## File:" headers)
- You CAN explain concepts fully documented above (Proof of Work 2.0, architecture)
- Be helpful and thorough when docs contain the information - only be brief when pointing to external links

STRICT Formatting Requirements (MUST follow exactly):
- DO NOT use markdown syntax (**bold**, *italic*, ```code blocks```, etc.)
- Use PLAIN TEXT formatting only:
  • Use UPPERCASE for emphasis (e.g., IMPORTANT, NOTE)
  • Use numbered lists: 1. 2. 3. for steps
  • Use bullet points: • or - for lists
  • Use indentation (2-4 spaces) to show hierarchy
  • Use blank lines to separate sections
  • Use dashes/equals for separators: -------- or ========
  • Show commands and code on separate indented lines
  • ALWAYS add blank line BEFORE and AFTER commands/code blocks for readability
  • Show URLs as plain text in angle brackets: <https://example.com>
- Keep formatting clean and readable in plain text terminals"""

            console.print(f"[green]✓ Loaded {len(context_parts)} markdown file(s) as context[/green]")
        else:
            console.print("[yellow]⚠ No documentation files found[/yellow]")

    except Exception as e:
        console.print(f"[yellow]⚠ Could not load repository context: {e}[/yellow]")

# Add system context to chat history if available
if repo_context:
    chat_history.append({"role": "system", "content": repo_context})

# =============================================================================
# WELCOME BANNER
# =============================================================================

welcome_text = Text()
welcome_text.append("🚀 ", style="bold cyan")
welcome_text.append("Gonka AI Chat Terminal", style="bold magenta")
welcome_text.append(" 🚀", style="bold cyan")

console.print(Panel(
    welcome_text,
    subtitle=f"Connected to: {NODE_URL}",
    border_style="magenta",
    padding=(1, 2)
))

tips_text = (
    "[cyan]💡 Tips:[/cyan]\n"
    "  • Type naturally - Gonka AI understands context\n"
)

if repo_context:
    tips_text += "  • [green]✓[/green] AI has loaded your repo docs - ask about your project!\n"

tips_text += (
    "  • Type [bold]/exit[/bold] to quit\n"
    f"  • Chat history is {'[green]being saved ✓[/green]' if args.save else '[dim]not being saved[/dim]'}\n"
)

console.print(tips_text)

# =============================================================================
# HELPER FUNCTIONS
# =============================================================================

def log_text(text: str):
    """Append text to the log file if --save flag is enabled."""
    if args.save and logfile:
        with open(logfile, "a", encoding="utf-8") as f:
            f.write(text + "\n")

# =============================================================================
# CHAT STREAMING FUNCTION
# =============================================================================

def stream_chat(prompt: str):
    """
    Stream AI responses in real-time with plain text formatting.

    Args:
        prompt: User's input message
    """
    # Add user message to conversation history
    chat_history.append({"role": "user", "content": prompt})
    log_text(f"👤 You: {prompt}")

    # Display AI response header
    console.print("\n[green]🤖 Gonka AI:[/green]\n")

    try:
        # Track timing and metrics
        start_time = time.time()
        first_chunk_time = None
        char_count = 0
        usage_info = None

        # Create streaming completion request
        stream = client.chat.completions.create(
            model=MODEL_NAME,
            messages=chat_history,
            stream=True,
            temperature=DEFAULT_TEMPERATURE
        )

        reply = ""

        # TRUE streaming - print each chunk immediately
        for event in stream:
            # Check for usage information (in last event) - do this FIRST before any continue
            if hasattr(event, 'usage') and event.usage:
                usage_info = event.usage

            # Validate event structure
            if not hasattr(event, "choices") or not event.choices:
                continue

            delta = getattr(event.choices[0], "delta", None)
            if not delta or not getattr(delta, "content", None):
                continue

            chunk = delta.content
            reply += chunk

            # Track first chunk time
            if first_chunk_time is None:
                first_chunk_time = time.time()

            char_count += len(chunk)

            # Stream immediately - no buffering
            print(chunk, end="", flush=True)

        print()  # Newline after response
        log_text(reply)

        # Display performance metrics
        end_time = time.time()
        total_duration = end_time - start_time
        if first_chunk_time:
            ttft = first_chunk_time - start_time  # Time To First Token
            generation_time = end_time - first_chunk_time  # Time spent generating tokens

            # Add blank line before metrics for spacing
            console.print()

            metrics = f"[bright_black]📊 Performance: Total {total_duration:.1f}s"
            metrics += f" · TTFT {ttft:.2f}s"

            # Add token information and calculate tokens/sec if available
            if usage_info:
                tokens = usage_info.completion_tokens
                metrics += f" · ↓ {tokens} tokens"

                # Calculate tokens per second during generation
                if generation_time > 0:
                    tokens_per_sec = tokens / generation_time
                    metrics += f" · {tokens_per_sec:.1f} tok/s"
            else:
                metrics += f" · ↓ {char_count} chars"

            metrics += "[/bright_black]\n"
            console.print(metrics)

        # Save assistant's response to history
        chat_history.append({"role": "assistant", "content": reply})
        log_text("")

    except KeyboardInterrupt:
        console.print("\n[red]⚠️  Response interrupted by user[/red]")
    except Exception as e:
        console.print(Panel(
            f"[red]Error communicating with Gonka AI:[/red]\n{str(e)}",
            title="❌ Error",
            border_style="red"
        ))
        log_text(f"[Error] {e}")

# =============================================================================
# MAIN CONVERSATION LOOP
# =============================================================================

def main():
    """Run the interactive chat loop."""
    try:
        while True:
            # Get user input
            try:
                user_input = input("\n👤 You > ").strip()
            except EOFError:
                break

            # Skip empty inputs
            if not user_input:
                continue

            # Handle exit commands
            if user_input.lower() in ["/exit", "exit", "quit", "/quit"]:
                console.print(Panel(
                    "[yellow]👋 Thanks for trying Gonka AI![/yellow]\n\n"
                    "[dim]Decentralized intelligence, democratized access.[/dim]",
                    title="Goodbye!",
                    border_style="yellow"
                ))
                if args.save:
                    console.print(f"[green]💾 Chat history saved to:[/green] [cyan]{logfile}[/cyan]")
                break

            # Process user message
            stream_chat(user_input)

    except KeyboardInterrupt:
        console.print("\n")
        console.print(Panel(
            "[yellow]Session interrupted by user.[/yellow]\n\n"
            "[dim]Come back anytime to experience Gonka AI![/dim]",
            title="👋 Goodbye",
            border_style="yellow"
        ))
        if args.save:
            console.print(f"[green]💾 Chat history saved to:[/green] [cyan]{logfile}[/cyan]")

if __name__ == "__main__":
    main()

