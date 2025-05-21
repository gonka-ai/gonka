import json
from typing_extensions import override

from bfcl.model_handler import utils as bfcl_utils
from bfcl.model_handler.oss_model.llama import LlamaHandler


DEFAULT_SYSTEM_PROMPT_WITHOUT_FUNC_DOC = """You are an expert in composing functions. You are given a question and a set of possible functions. Based on the question, you will need to make one or more function/tool calls to achieve the purpose.
If none of the function can be used, point it out. If the given question lacks the parameters required by the function, also point it out.
You should only return the function calls in your response.

If you decide to invoke any of the function(s), you MUST put it in the format of [{{"name": "func_name1", "parameters": {{"key11": value11, "key12": value12}}}}, ...]
You SHOULD NOT include any other text in the response.

At each turn, your should try your best to complete the tasks requested by the user within the current turn. Continue to output functions to call until you have fulfilled the user's request to the best of your ability. Once you have no more functions to call, the system will consider the current turn complete and proceed to the next turn or task.
"""

DEFAULT_SYSTEM_PROMPT = (
    DEFAULT_SYSTEM_PROMPT_WITHOUT_FUNC_DOC
    + """
Here is a list of functions in JSON format that you can invoke.\n{functions}\n
"""
)
class TrainLLamaHandler(LlamaHandler):
    TYPE_MAPPING = {
        'str': 'string',
        'int': 'integer',
        'bool': 'boolean',
        'float': 'number',
    }

    def __init__(self, tokenizer, model_name=None):
        super().__init__(tokenizer.name_or_path, 1)
        if model_name != None:
            self.model_name = model_name

        self.tokenizer = tokenizer
        self.max_context_length = 12000

    
    def format_train_output(
        self,
        x: dict
    ):
        answers = self.parse_tools_or_answers(x['answers'])
        for d in answers:
            if "arguments" in d:
                d["parameters"] = d["arguments"]
                del d["arguments"]
    
        return json.dumps(answers) + self.tokenizer.eos_token
    
    def format_train_input(
        self,
        x: dict
    ):
        if "tools" in x:
            tools = self.parse_tools_or_answers(x['tools'])
            function_list = []
            for tool in tools:
                function_item = {
                    'name': tool['name'],
                    'description': tool['description'],
                    'parameters': self.transform_parameters(tool['parameters']),
                }
                function_list.append(function_item)

            transformed_data = {
                'id': "train_" + str(x['id']),
                'function': function_list,
                'question': [[
                    {'role': 'user', 'content': x['query']}
                ]],
            }
        else:
            transformed_data = x
            transformed_data['question'] = [[ y for y in item if y['role'] != 'system'] for item in x['question']]
        
        inference_data: dict = self._pre_query_processing_prompting(transformed_data)
        inference_data = self.add_first_turn_message_prompting(
            inference_data, transformed_data["question"]
        )
        function: list[dict] = inference_data["function"]
        message: list[dict] = inference_data["message"][0]

        return self._format_prompt(message, function)

    def format_train_all(self, x: dict):
        return self.format_train_input(x) + self.format_train_output(x)
        
    @staticmethod
    def parse_tools_or_answers(data_str):
        data_str = data_str.replace('True', 'true').replace('False', 'false').replace('None', 'null')
        return json.loads(data_str)

    @staticmethod
    def transform_parameters(params):
        transformed = {
            'type': 'dict',
            'properties': {},
        }
        required = []
        for param_name, param_info in params.items():
            # Extract and clean the type
            param_type = param_info['type'].split(',')[0].strip()
            # Map to JSON Schema type
            json_type = TrainLLamaHandler.TYPE_MAPPING.get(param_type, 'string')
            # Build the property
            property_info = {
                'type': json_type,
                'description': param_info['description'],
            }
            transformed['properties'][param_name] = property_info
            # Check if the parameter is required
            if 'optional' not in param_info['type']:
                required.append(param_name)
        if required:
            transformed['required'] = required
        return transformed

    @staticmethod
    def transform_answers(answers):
        res = []
        for x in answers:
            args = [f"{arg_name}={arg_value}" for (arg_name, arg_value) in x["arguments"].items()]
            args = ", ".join(args)
            answer = f"{x['name']}({args})"
            res.append(answer)
        
        return res
                

    @override
    def _pre_query_processing_prompting(self, test_entry: dict) -> dict:
        functions: list = test_entry["function"]
        test_category: str = test_entry["id"].rsplit("_", 1)[0]

        functions = bfcl_utils.func_doc_language_specific_pre_processing(functions, test_category)

        test_entry["question"][0] = bfcl_utils.system_prompt_pre_processing_chat_model(
            test_entry["question"][0], functions, test_category, DEFAULT_SYSTEM_PROMPT
        )

        return {"message": [], "function": functions}