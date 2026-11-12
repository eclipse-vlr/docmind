"""HTTP evaluation of an isolated DocMind instance; paid calls need an explicit flag.

This measures expected evidence/fact coverage and refusal separately. It does
not prove that every answer claim is supported; human review remains explicit.
"""
import argparse
import json
import subprocess
from pathlib import Path
import time
import urllib.request
import uuid

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base-url',required=True)
    parser.add_argument('--dataset',type=Path,default=Path(__file__).resolve().parents[1]/'testdata/rag-eval.json')
    parser.add_argument('--report',type=Path,required=True)
    parser.add_argument('--search-cli',type=Path,required=True,help='Path to a built docmind-search using the same DB, embedding settings and budget as the isolated HTTP instance')
    parser.add_argument('--allow-model-calls',action='store_true',help='Allow calls to the configured instance; use fake providers or set prices and budget first')
    args=parser.parse_args()
    if not args.allow_model_calls:parser.error('--allow-model-calls is required; the instance may use paid providers')
    cases=json.loads(args.dataset.read_text(encoding='utf-8'))
    # Reserve a new report before any remote work. Persist each completed case.
    with args.report.open('x',encoding='utf-8') as report:
        def call(method,path,body=None,content='application/json'):
            request=urllib.request.Request(args.base_url.rstrip('/')+path,data=body,method=method,headers={'Content-Type':content})
            with urllib.request.urlopen(request,timeout=40) as response:return json.load(response)
        for case in cases:
            ids=[]
            try:
                for index,text in enumerate(case['documents']):
                    boundary=uuid.uuid4().hex
                    data=(f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="{case["id"]}-{index}.txt"\r\nContent-Type: text/plain\r\n\r\n{text}\r\n--{boundary}--\r\n').encode()
                    document=call('POST','/api/v1/documents',data,'multipart/form-data; boundary='+boundary)
                    ids.append(document['id'])
                deadline=time.monotonic()+180
                for id in ids:
                    while True:
                        document=call('GET','/api/v1/documents/'+id)
                        if document['status']=='ready':break
                        if document['status']=='failed' or time.monotonic()>deadline:raise RuntimeError('Index did not become ready')
                        time.sleep(.2)
                search=subprocess.run([str(args.search_cli),'-question',case['question'],'-documents',','.join(ids),'-top-k','5'],check=True,capture_output=True,text=True,encoding='utf-8',timeout=40)
                retrieved=json.loads(search.stdout)['matches']
                evidence='\n'.join(match['snippet'] for match in retrieved)
                result=call('POST','/api/v1/questions',json.dumps({'question':case['question'],'document_ids':ids}).encode())
                answer=result['answer']
                output={'id':case['id'],'retrieved':retrieved,'result':result,'retrieval_hits':sum(e in evidence for e in case['evidence']),'retrieval_expected':len(case['evidence']),'fact_hits':sum(f.casefold() in answer.casefold() for f in case['facts']),'facts_expected':len(case['facts']),'refusal_correct':(not result['sources'])==case['refuse'],'forbidden_hits':[f for f in case.get('forbidden',[]) if f.casefold() in answer.casefold()],'groundedness_human_review':'pending','human_checks':case.get('human_checks',[])}
                report.write(json.dumps(output,ensure_ascii=False)+'\n');report.flush()
            finally:
                for id in ids:
                    request=urllib.request.Request(args.base_url.rstrip('/')+'/api/v1/documents/'+id,method='DELETE')
                    with urllib.request.urlopen(request,timeout=10):pass

if __name__=='__main__':main()
