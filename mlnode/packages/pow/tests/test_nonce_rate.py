#!/usr/bin/env python3
"""
Nonce Generation Rate Test

Simple Python script to measure nonce generation rates across different GPU configurations.
Usage: CUDA_VISIBLE_DEVICES=0 python tests/test_nonce_rate.py
"""

import os
import sys
import time
import signal
import subprocess
import requests
import json
from pathlib import Path

# Add src to Python path
script_dir = Path(__file__).parent
project_root = script_dir.parent
sys.path.insert(0, str(project_root / "src"))
sys.path.insert(0, str(project_root.parent / "common" / "src"))

from pow.models.utils import PARAMS_V1, PARAMS_V2


class NonceRateTest:
    def __init__(self, port=8085, test_duration=90, params=PARAMS_V1):
        self.port = port
        self.test_duration = test_duration
        self.server_process = None
        self.params = params
        self.log_file = "/tmp/nonce_test.log"
        self.local_log_file = "nonce_test_debug.log"
        self.tee_thread = None
        self.log_file_handle = None
        self.stop_tee = None
        
    def cleanup_existing_processes(self):
        """Kill any existing PoW processes"""
        print("Cleaning up existing processes...")
        
        # Kill uvicorn and python PoW processes
        subprocess.run(["pkill", "-f", "uvicorn.*pow"], check=False)
        subprocess.run(["pkill", "-f", "python.*pow"], check=False)
        
        # Kill GPU processes
        try:
            result = subprocess.run([
                "nvidia-smi", "--query-compute-apps=pid", 
                "--format=csv,noheader,nounits"
            ], capture_output=True, text=True, check=False)
            
            if result.returncode == 0 and result.stdout.strip():
                pids = result.stdout.strip().split('\n')
                for pid in pids:
                    if pid.strip():
                        subprocess.run(["kill", "-9", pid.strip()], check=False)
        except FileNotFoundError:
            pass  # nvidia-smi not available
            
        time.sleep(3)
        
    def start_server(self):
        """Start the PoW server"""
        print(f"Starting server on port {self.port}...")
        
        cmd = [
            "python3", "-m", "uvicorn", "pow.service.app:app",
            "--host", "0.0.0.0", "--port", str(self.port),
            "--log-level", "info"
        ]
        
        # Start server with pipes to capture output
        self.server_process = subprocess.Popen(
            cmd, env=os.environ, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, 
            universal_newlines=True, bufsize=1
        )
        
        # Start a thread to read and tee the output
        import threading
        self.log_file_handle = open(self.log_file, "w")
        self.stop_tee = threading.Event()
        
        def tee_output():
            try:
                for line in iter(self.server_process.stdout.readline, ''):
                    if self.stop_tee.is_set():
                        break
                        
                    # Write to stdout
                    sys.stdout.write(line)
                    sys.stdout.flush()
                    
                    # Write to file if still open
                    if self.log_file_handle and not self.stop_tee.is_set():
                        try:
                            self.log_file_handle.write(line)
                            self.log_file_handle.flush()
                        except (AttributeError, ValueError, OSError):
                            # File was closed, stop the loop
                            break
            except Exception:
                # Silently handle any other exceptions in the thread
                pass
        
        self.tee_thread = threading.Thread(target=tee_output, daemon=True)
        self.tee_thread.start()
            
        print(f"Server started with PID: {self.server_process.pid}")
        time.sleep(8)  # Wait for server to start
        
    def init_generation(self):
        """Initialize nonce generation with high R_TARGET"""
        print("Initializing generation...")
        
        payload = {
            "node_id": 0,
            "node_count": 1,
            "block_hash": "test",
            "block_height": 1,
            "public_key": "test",
            "batch_size": 500,
            "r_target": 1.4013564660458173,
            "fraud_threshold": 0.01,
            "params": self.params.__dict__,
            "url": "http://localhost:5000"
        }
        
        try:
            response = requests.post(
                f"http://localhost:{self.port}/api/v1/pow/init/generate",
                json=payload,
                timeout=30
            )
            response.raise_for_status()
            print("Generation initialized successfully")
        except requests.RequestException as e:
            print(f"Failed to initialize generation: {e}")
            raise
            
    def wait_and_measure(self):
        """Wait for test duration and extract results"""
        print(f"Generating... ({self.test_duration}s + model init time)")
        time.sleep(self.test_duration)
        
    def extract_results(self):
        """Extract nonce rate from log file and aggregate all workers"""
        print("\nResults:")
        
        try:
            with open(self.log_file, "r") as f:
                lines = f.readlines()
                
            # Save logs locally for debugging
            with open(self.local_log_file, "w") as f:
                f.writelines(lines)
            print(f"Debug logs saved to: {self.local_log_file}")
            
            # Analyze logs for worker creation and errors
            self._analyze_logs(lines)
                
            # Find all worker rate entries - now in format "[X] Generated: ... (Y.Z valid/min, A.B raw/min)"
            nonce_lines = [line for line in lines if "valid/min" in line and "raw/min" in line and "Generated:" in line]
            if nonce_lines:
                total_valid_rate = 0.0
                total_raw_rate = 0.0
                worker_valid_rates = {}
                worker_raw_rates = {}
                
                print(f"Found {len(nonce_lines)} worker rate lines:")
                for line in nonce_lines[:3]:  # Show first 3 for debugging
                    print(f"  {line.strip()}")
                
                # Extract rates from each worker
                for line in nonce_lines:
                    # Parse "[X] Generated: ... (Y.Z valid/min, A.B raw/min)"
                    try:
                        # Look for the worker ID pattern in the log line
                        # The line might be: "timestamp - module - INFO - [X] Generated: ..."
                        if "[" in line and "] Generated:" in line:
                            # Find the worker ID between [ and ] that comes before "Generated:"
                            parts = line.split("] Generated:")
                            if len(parts) >= 2:
                                worker_part = parts[0]
                                if "[" in worker_part:
                                    worker_id = worker_part.split("[")[-1]  # Get the last [X] part
                                    
                                    # Extract valid rate from (Y.Z valid/min, ...)
                                    if "(" in line and "valid/min" in line:
                                        rate_section = line.split("(")[-1]  # Get everything after the last (
                                        if "valid/min" in rate_section:
                                            valid_rate_part = rate_section.split("valid/min")[0].strip()
                                            valid_rate = float(valid_rate_part)
                                            worker_valid_rates[worker_id] = max(worker_valid_rates.get(worker_id, 0), valid_rate)
                                    
                                    # Extract raw rate from (, A.B raw/min)
                                    if "raw/min)" in line:
                                        raw_rate_section = line.split("raw/min)")[0]  # Get everything before raw/min)
                                        if ", " in raw_rate_section:
                                            raw_rate_part = raw_rate_section.split(", ")[-1].strip()  # Get part after last comma
                                            raw_rate = float(raw_rate_part)
                                            worker_raw_rates[worker_id] = max(worker_raw_rates.get(worker_id, 0), raw_rate)
                    except (IndexError, ValueError) as e:
                        print(f"  Parse error: {e} for line: {line.strip()}")
                        continue
                
                # Print individual worker rates and calculate totals
                print("Individual worker rates:")
                for worker_id in sorted(set(worker_valid_rates.keys()) | set(worker_raw_rates.keys())):
                    valid_rate = worker_valid_rates.get(worker_id, 0)
                    raw_rate = worker_raw_rates.get(worker_id, 0)
                    print(f"  Worker {worker_id}: {valid_rate:.1f} valid/min, {raw_rate:.1f} raw/min")
                    total_valid_rate += valid_rate
                    total_raw_rate += raw_rate
                
                print(f"\nTotal valid rate: {total_valid_rate:.1f} nonces/min")
                print(f"Total raw rate: {total_raw_rate:.1f} nonces/min")
                print(f"Workers: {len(set(worker_valid_rates.keys()) | set(worker_raw_rates.keys()))}")
                print(f"Success ratio: 1 in {total_raw_rate/total_valid_rate:.0f}" if total_valid_rate > 0 else "Success ratio: N/A")
            else:
                print("No worker nonce rates found in logs")
                
        except FileNotFoundError:
            print("Log file not found")
            
    def _analyze_logs(self, lines):
        """Analyze logs for worker creation, errors, and GPU detection"""
        print("\nLog Analysis:")
        
        # Check GPU detection
        gpu_lines = [line for line in lines if "cuda" in line.lower() and ("device" in line.lower() or "gpu" in line.lower())]
        if gpu_lines:
            print("GPU Detection:")
            for line in gpu_lines[:5]:  # Show first 5 GPU-related lines
                print(f"  {line.strip()}")
        
        # Check worker creation
        worker_creation_lines = [line for line in lines if "Worker" in line and ("start" in line.lower() or "creat" in line.lower() or "init" in line.lower())]
        if worker_creation_lines:
            print("Worker Creation:")
            for line in worker_creation_lines:
                print(f"  {line.strip()}")
        
        # Check for errors
        error_lines = [line for line in lines if any(keyword in line.lower() for keyword in ["error", "exception", "failed", "crash"])]
        if error_lines:
            print("Errors Found:")
            for line in error_lines:
                print(f"  {line.strip()}")
        else:
            print("No errors found in logs")
            
        # Check controller/device info
        controller_lines = [line for line in lines if "controller" in line.lower() or "batch size" in line.lower()]
        if controller_lines:
            print("Controller Info:")
            for line in controller_lines:
                print(f"  {line.strip()}")
            
    def cleanup(self):
        """Clean up processes and files"""
        print("Cleaning up...")
        
        # Signal the tee thread to stop
        if self.stop_tee:
            self.stop_tee.set()
            
        # Close log file handle
        if self.log_file_handle:
            try:
                self.log_file_handle.close()
            except:
                pass
            self.log_file_handle = None
                
        if self.server_process:
            self.server_process.terminate()
            try:
                self.server_process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.server_process.kill()
                
        # Wait for tee thread to finish
        if self.tee_thread and self.tee_thread.is_alive():
            self.tee_thread.join(timeout=1.0)
                
        # Kill any remaining processes
        subprocess.run(["pkill", "-f", "uvicorn.*pow"], check=False)
        subprocess.run(["pkill", "-f", "python.*pow"], check=False)
        subprocess.run(["pkill", "-9", "-f", "uvicorn.*pow"], check=False)
        subprocess.run(["pkill", "-9", "-f", "python.*pow"], check=False)
        
        # Kill GPU processes
        try:
            result = subprocess.run([
                "nvidia-smi", "--query-compute-apps=pid", 
                "--format=csv,noheader,nounits"
            ], capture_output=True, text=True, check=False)
            
            if result.returncode == 0 and result.stdout.strip():
                pids = result.stdout.strip().split('\n')
                for pid in pids:
                    if pid.strip():
                        subprocess.run(["kill", "-9", pid.strip()], check=False)
        except FileNotFoundError:
            pass
            
        time.sleep(2)
        print("✅ Cleanup completed")
        
    def run(self):
        """Run the complete test"""
        gpu_devices = os.environ.get("CUDA_VISIBLE_DEVICES", "all")
        print("Testing nonce generation rate...")
        print(f"GPU: {gpu_devices}")
        
        try:
            self.cleanup_existing_processes()
            self.start_server()
            self.init_generation()
            self.wait_and_measure()
            self.extract_results()
        except KeyboardInterrupt:
            print("\nTest interrupted by user")
        except Exception as e:
            print(f"Test failed: {e}")
        finally:
            self.cleanup()


def main():
    """Main entry point"""
    params_env = os.environ.get("PARAMS", "v1")
    if params_env == "v1":
        params = PARAMS_V1
    elif params_env == "v2":
        params = PARAMS_V2
    else:
        raise ValueError(f"Invalid params: {params_env}")
    
    test = NonceRateTest(test_duration=120, params=params)
    
    def signal_handler(signum, frame):
        print("\nReceived signal, cleaning up...")
        test.cleanup()
        sys.exit(0)
        
    signal.signal(signal.SIGINT, signal_handler)
    signal.signal(signal.SIGTERM, signal_handler)
    
    test.run()


if __name__ == "__main__":
    main()
