from fastapi import FastAPI
from zeroband.service.routes import router, API_PREFIX
from zeroband.service.manager import TrainManager

app = FastAPI()

@app.on_event("startup")
async def startup_event():
    app.state.train_manager = TrainManager()

app.include_router(router, prefix=API_PREFIX)